package convert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Derived-artifact cache.
//
// Conversion is slow — minutes for a large PDF — so a converted EPUB is cached
// and reused. The key is (source content hash, converter identity): changing
// the source or switching converters produces a different artifact, while
// re-running the same conversion is free.
//
// Everything here is disposable. Deleting the cache directory costs time on the
// next run and nothing else, which is the same contract as the SQLite index.

// Cache stores converted artifacts under a root directory.
type Cache struct {
	Root string
}

// NewCache returns a cache rooted at dir.
func NewCache(dir string) *Cache { return &Cache{Root: dir} }

// Entry is the metadata stored beside each cached artifact.
//
// It records the source path and hash so a derived EPUB stays linked to the
// book it came from, which is what lets sync record the relationship.
type Entry struct {
	SourcePath   string     `json:"source_path"`
	SourceSHA256 string     `json:"source_sha256"`
	SourceSize   int64      `json:"source_size"`
	Converter    string     `json:"converter"`
	OutputSize   int64      `json:"output_size"`
	ConvertedAt  int64      `json:"converted_unix"`
	Assessment   Assessment `json:"assessment"`
}

// key derives the cache key for a source hash and converter.
//
// The converter's argument list is folded in, not just its name: changing
// --enable-heuristics changes the output, and reusing an artifact produced
// under different settings would be wrong.
func key(sourceSHA string, p Preset) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", sourceSHA, p.Name)
	for _, a := range p.Args {
		fmt.Fprintf(h, "%s\x00", a)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// paths returns the artifact and metadata paths for a key.
func (c *Cache) paths(k string) (artifact, meta string) {
	// One level of fan-out keeps directory listings manageable on a large
	// library without making paths hard to inspect by hand.
	dir := filepath.Join(c.Root, k[:2])
	return filepath.Join(dir, k+".epub"), filepath.Join(dir, k+".json")
}

// Lookup returns the cached artifact for a source, if present and intact.
func (c *Cache) Lookup(sourceSHA string, p Preset) (string, *Entry, bool) {
	artifact, meta := c.paths(key(sourceSHA, p))

	info, err := os.Stat(artifact)
	if err != nil || info.Size() == 0 {
		return "", nil, false
	}

	data, err := os.ReadFile(meta)
	if err != nil {
		// The artifact exists but its metadata does not. Treat it as a miss
		// rather than returning an entry with invented fields.
		return "", nil, false
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return "", nil, false
	}
	return artifact, &e, true
}

// Put stores a converted artifact, moving it into the cache.
func (c *Cache) Put(sourceSHA string, p Preset, result *Result, sourceSize int64) (string, error) {
	k := key(sourceSHA, p)
	artifact, meta := c.paths(k)

	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		return "", fmt.Errorf("convert: create cache directory: %w", err)
	}

	if err := moveOrCopy(result.Output, artifact); err != nil {
		return "", err
	}

	entry := Entry{
		SourcePath:   result.Source,
		SourceSHA256: sourceSHA,
		SourceSize:   sourceSize,
		Converter:    result.Converter,
		OutputSize:   result.OutputSize,
		ConvertedAt:  time.Now().Unix(),
		Assessment:   result.Assessment,
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(meta, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("convert: write cache metadata: %w", err)
	}
	return artifact, nil
}

// Convert returns a converted EPUB for src, reusing the cache when possible.
//
// This is the entry point callers should use; it hashes the source, checks the
// cache, and converts only on a miss.
func (c *Cache) Convert(ctx context.Context, conv *Converter, src string, opts Options) (string, *Entry, bool, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", nil, false, fmt.Errorf("convert: %w", err)
	}

	sum, err := HashFile(src)
	if err != nil {
		return "", nil, false, err
	}

	if artifact, entry, ok := c.Lookup(sum, conv.Preset); ok {
		return artifact, entry, true, nil
	}

	// Convert into a temporary location, then move into the cache, so a
	// concurrent reader never observes a partial artifact.
	tmpDir, err := os.MkdirTemp(c.Root, ".pending-*")
	if err != nil {
		if err := os.MkdirAll(c.Root, 0o755); err != nil {
			return "", nil, false, err
		}
		if tmpDir, err = os.MkdirTemp(c.Root, ".pending-*"); err != nil {
			return "", nil, false, err
		}
	}
	defer os.RemoveAll(tmpDir)

	staged := filepath.Join(tmpDir, "out.epub")
	result, err := conv.Convert(ctx, src, staged, opts)
	if err != nil {
		return "", nil, false, err
	}

	artifact, err := c.Put(sum, conv.Preset, result, info.Size())
	if err != nil {
		return "", nil, false, err
	}

	_, entry, _ := c.Lookup(sum, conv.Preset)
	return artifact, entry, false, nil
}

// Prune removes cached artifacts older than maxAge, reporting how many went.
func (c *Cache) Prune(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge)
	var removed int

	entries, err := os.ReadDir(c.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	for _, shard := range entries {
		if !shard.IsDir() {
			continue
		}
		dir := filepath.Join(c.Root, shard.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			info, err := f.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			if os.Remove(filepath.Join(dir, f.Name())) == nil && filepath.Ext(f.Name()) == ".epub" {
				removed++
			}
		}
		// Drop the shard directory once it is empty.
		if remaining, err := os.ReadDir(dir); err == nil && len(remaining) == 0 {
			os.Remove(dir)
		}
	}
	return removed, nil
}

// HashFile computes the SHA-256 of a file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("convert: open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("convert: hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// moveOrCopy renames a file, falling back to copy across filesystems.
func moveOrCopy(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".cache-*")
	if err != nil {
		return err
	}
	name := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		os.Remove(name)
		return err
	}
	return os.Remove(src)
}
