package convert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// registerBuiltin installs a compiled-in converter for the duration of a test.
//
// Cache behaviour needs a converter whose runs are countable and whose output
// is fixed. Before conversion moved in-process this was a shell snippet; now it
// is a Go function, which is both faster and one less reason for the test suite
// to depend on anything outside the binary.
func registerBuiltin(t *testing.T, name string, run func(ctx context.Context, src, dst string) (string, error)) {
	t.Helper()
	if _, exists := builtins[name]; exists {
		t.Fatalf("built-in %q already registered", name)
	}
	builtins[name] = run
	t.Cleanup(func() { delete(builtins, name) })
}

// cacheConverter builds a converter that copies a prepared EPUB and counts how
// many times it actually ran, so cache hits are observable.
func cacheConverter(t *testing.T, dir string, runs *int) *Converter {
	t.Helper()

	built := filepath.Join(dir, "built.epub")
	minimalEPUB(t, built, []string{strings.Repeat("Call me Ishmael. ", 100)}, 0, 0)

	name := "counting"
	registerBuiltin(t, name, func(_ context.Context, _, dst string) (string, error) {
		*runs++
		data, err := os.ReadFile(built)
		if err != nil {
			return "", err
		}
		return "", os.WriteFile(dst, data, 0o644)
	})

	return &Converter{
		Preset: Preset{
			Name:    name,
			Args:    []string{"--fixed"},
			Formats: []string{"pdf"},
		},
		Timeout: 30 * time.Second,
	}
}

func TestCacheReusesConvertedArtifact(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	var runs int
	conv := cacheConverter(t, dir, &runs)
	cache := NewCache(filepath.Join(dir, "cache"))

	artifact, entry, cached, err := cache.Convert(context.Background(), conv, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Error("the first conversion cannot be a cache hit")
	}
	if entry == nil || entry.SourceSHA256 == "" {
		t.Fatalf("entry = %+v; the source hash links the artifact back to its book", entry)
	}
	if entry.Assessment.Quality != QualityGood {
		t.Errorf("assessment not stored: %+v", entry.Assessment)
	}
	if runs != 1 {
		t.Fatalf("converter ran %d times, want 1", runs)
	}

	// Second call: same source, same converter. Must not re-run.
	artifact2, entry2, cached2, err := cache.Convert(context.Background(), conv, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !cached2 {
		t.Error("the second conversion should be a cache hit")
	}
	if artifact2 != artifact {
		t.Errorf("artifact path changed: %q then %q", artifact, artifact2)
	}
	if entry2.SourceSHA256 != entry.SourceSHA256 {
		t.Error("cached entry does not match")
	}
	if n := runs; n != 1 {
		t.Errorf("converter ran %d times; a cache hit must not re-run it", n)
	}
}

// Changing the source must miss the cache, or an edited book would keep
// returning its stale conversion.
func TestCacheMissesWhenSourceChanges(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	var runs int
	conv := cacheConverter(t, dir, &runs)
	cache := NewCache(filepath.Join(dir, "cache"))

	if _, _, _, err := cache.Convert(context.Background(), conv, src, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("different content entirely"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, cached, err := cache.Convert(context.Background(), conv, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Error("a changed source must not hit the cache")
	}
	if n := runs; n != 2 {
		t.Errorf("converter ran %d times, want 2", n)
	}
}

// The cache key folds in the converter's settings, not just its name. Changing
// a setting changes the output, and reusing an artifact produced under
// different settings would be wrong.
func TestCacheKeyDependsOnConverterArguments(t *testing.T) {
	a := Preset{Name: "same", Args: []string{"--profile=crosspoint"}}
	b := Preset{Name: "same", Args: []string{"--profile=crosspoint", "--keep-vectors"}}

	if key("deadbeef", a) == key("deadbeef", b) {
		t.Error("presets differing only in arguments share a cache key")
	}
	if key("deadbeef", a) != key("deadbeef", a) {
		t.Error("key is not deterministic")
	}
	if key("aaaa", a) == key("bbbb", a) {
		t.Error("different sources share a cache key")
	}
}

// An artifact whose metadata is missing is a miss, not an entry with invented
// fields.
func TestCacheTreatsOrphanedArtifactAsMiss(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	var runs int
	conv := cacheConverter(t, dir, &runs)
	cache := NewCache(filepath.Join(dir, "cache"))

	if _, _, _, err := cache.Convert(context.Background(), conv, src, Options{}); err != nil {
		t.Fatal(err)
	}

	sum, err := HashFile(src)
	if err != nil {
		t.Fatal(err)
	}
	_, meta := cache.paths(key(sum, conv.Preset))
	if err := os.Remove(meta); err != nil {
		t.Fatal(err)
	}

	if _, _, ok := cache.Lookup(sum, conv.Preset); ok {
		t.Error("an artifact with no metadata should be a miss")
	}
}

func TestCachePrune(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	var runs int
	conv := cacheConverter(t, dir, &runs)
	cache := NewCache(filepath.Join(dir, "cache"))

	if _, _, _, err := cache.Convert(context.Background(), conv, src, Options{}); err != nil {
		t.Fatal(err)
	}

	// Nothing is old enough yet.
	if n, err := cache.Prune(time.Hour); err != nil || n != 0 {
		t.Errorf("Prune(1h) = %d, %v; want 0", n, err)
	}

	// Everything is older than zero.
	n, err := cache.Prune(0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Prune(0) removed %d artifacts, want 1", n)
	}

	sum, _ := HashFile(src)
	if _, _, ok := cache.Lookup(sum, conv.Preset); ok {
		t.Error("the pruned artifact is still reported as cached")
	}
}

func TestPruneOnMissingCacheIsNotAnError(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "never-created"))
	if n, err := cache.Prune(time.Hour); err != nil || n != 0 {
		t.Errorf("Prune on a missing cache = %d, %v", n, err)
	}
}

// A failed conversion must not populate the cache, or the failure would be
// served back on every later attempt.
func TestFailedConversionIsNotCached(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "book.pdf")

	registerBuiltin(t, "failing", func(context.Context, string, string) (string, error) {
		return "", errors.New("converter refused this book")
	})
	conv := &Converter{
		Preset:  Preset{Name: "failing", Formats: []string{"pdf"}},
		Timeout: 10 * time.Second,
	}
	cache := NewCache(filepath.Join(dir, "cache"))

	if _, _, _, err := cache.Convert(context.Background(), conv, src, Options{}); err == nil {
		t.Fatal("expected the conversion to fail")
	}

	sum, _ := HashFile(src)
	if _, _, ok := cache.Lookup(sum, conv.Preset); ok {
		t.Error("a failed conversion populated the cache")
	}

	// And it left no pending directories behind.
	entries, _ := os.ReadDir(filepath.Join(dir, "cache"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pending-") {
			t.Errorf("staging directory left behind: %s", e.Name())
		}
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	os.WriteFile(a, []byte("same"), 0o644)
	os.WriteFile(b, []byte("same"), 0o644)

	ha, err := HashFile(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := HashFile(b)
	if ha != hb {
		t.Error("identical content produced different hashes")
	}
	if len(ha) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(ha))
	}
	if _, err := HashFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected an error for a missing file")
	}
}
