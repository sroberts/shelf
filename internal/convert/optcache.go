package convert

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Optimized-artifact cache.
//
// Keyed by (source hash, profile identity), per spec.md 9.2. Both halves
// matter, and for different reasons:
//
//   - the source hash means editing a book's metadata produces a new artifact
//     rather than silently reusing a stale one;
//   - the profile identity means an unchanged profile produces the same key,
//     which is what stops sync from re-uploading a library after every run.
//
// The manifest records the hash of the bytes actually sent, so a cache hit and
// a fresh optimization must produce identical output for the same inputs. That
// is why the optimizer takes no clock, no randomness, and no ambient config.

// OptimizeCache stores optimized EPUBs under a root directory.
type OptimizeCache struct {
	Root string
}

// NewOptimizeCache returns a cache rooted at dir, which should be
// config.Paths.OptimizedDir().
func NewOptimizeCache(dir string) *OptimizeCache { return &OptimizeCache{Root: dir} }

// OptimizeEntry is the metadata stored beside each optimized artifact.
type OptimizeEntry struct {
	SourcePath   string `json:"source_path"`
	SourceSHA256 string `json:"source_sha256"`
	SourceSize   int64  `json:"source_size"`
	Profile      string `json:"profile"`
	// OutputSHA256 is the hash of the bytes a sync would actually upload. The
	// manifest records this value, not the source hash.
	OutputSHA256 string `json:"output_sha256"`
	OutputSize   int64  `json:"output_size"`
	Images       int    `json:"images"`
	Rewritten    int    `json:"images_rewritten"`
	OptimizedAt  int64  `json:"optimized_unix"`
}

// optKey derives the cache key for a source hash and profile.
func optKey(sourceSHA string, p Profile) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s", sourceSHA, p.identity())
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// paths returns the artifact and metadata paths for a key.
func (c *OptimizeCache) paths(k string) (artifact, meta string) {
	// One level of fan-out, matching the converted-artifact cache, so a large
	// library does not produce an unlistable directory.
	dir := filepath.Join(c.Root, k[:2])
	return filepath.Join(dir, k+".epub"), filepath.Join(dir, k+".json")
}

// Lookup returns the cached artifact for a source and profile, if intact.
func (c *OptimizeCache) Lookup(sourceSHA string, p Profile) (string, *OptimizeEntry, bool) {
	artifact, meta := c.paths(optKey(sourceSHA, p))

	info, err := os.Stat(artifact)
	if err != nil || info.Size() == 0 {
		return "", nil, false
	}

	data, err := os.ReadFile(meta)
	if err != nil {
		// Artifact without metadata: treat as a miss rather than returning an
		// entry with invented fields. Re-optimizing is cheap; a wrong
		// OutputSHA256 in the manifest is not.
		return "", nil, false
	}
	var e OptimizeEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return "", nil, false
	}
	return artifact, &e, true
}

// Optimize returns an optimized EPUB for src, reusing the cache when possible.
//
// This is the entry point callers should use. The bool reports a cache hit.
func (c *OptimizeCache) Optimize(src string, p Profile) (string, *OptimizeEntry, bool, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", nil, false, fmt.Errorf("convert: %w", err)
	}

	sum, err := HashFile(src)
	if err != nil {
		return "", nil, false, err
	}

	if artifact, entry, ok := c.Lookup(sum, p); ok {
		return artifact, entry, true, nil
	}

	// Optimize into a temporary location and move it in, so a concurrent
	// reader never observes a partial artifact.
	if err := os.MkdirAll(c.Root, 0o755); err != nil {
		return "", nil, false, fmt.Errorf("convert: create cache directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(c.Root, ".pending-*")
	if err != nil {
		return "", nil, false, err
	}
	defer os.RemoveAll(tmpDir)

	staged := filepath.Join(tmpDir, "out.epub")
	res, err := Optimize(src, staged, p)
	if err != nil {
		return "", nil, false, err
	}

	outSum, err := HashFile(staged)
	if err != nil {
		return "", nil, false, err
	}

	entry := OptimizeEntry{
		SourcePath:   src,
		SourceSHA256: sum,
		SourceSize:   info.Size(),
		Profile:      p.Name,
		OutputSHA256: outSum,
		OutputSize:   res.OutputSize,
		Images:       res.Images,
		Rewritten:    res.Rewritten,
		OptimizedAt:  time.Now().Unix(),
	}

	artifact, err := c.put(sum, p, staged, entry)
	if err != nil {
		return "", nil, false, err
	}
	return artifact, &entry, false, nil
}

// put moves a staged artifact and its metadata into the cache.
func (c *OptimizeCache) put(sourceSHA string, p Profile, staged string, e OptimizeEntry) (string, error) {
	artifact, meta := c.paths(optKey(sourceSHA, p))

	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		return "", fmt.Errorf("convert: create cache directory: %w", err)
	}
	if err := moveOrCopy(staged, artifact); err != nil {
		return "", err
	}

	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(meta, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("convert: write cache metadata: %w", err)
	}
	return artifact, nil
}

// Prune removes optimized artifacts older than maxAge, reporting how many went.
func (c *OptimizeCache) Prune(maxAge time.Duration) (int, error) {
	return (&Cache{Root: c.Root}).Prune(maxAge)
}
