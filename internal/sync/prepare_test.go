package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sroberts/shelf/internal/convert"
	"github.com/sroberts/shelf/internal/device"
)

// syncableFormats is what the CrossPoint firmware renders directly.
func syncableFormats(format string) bool {
	switch format {
	case "epub", "txt", "xtc":
		return true
	}
	return false
}

func testPreparer(t *testing.T, optimize bool) *Preparer {
	t.Helper()
	dir := t.TempDir()

	profile, err := convert.LookupProfile("x4")
	if err != nil {
		t.Fatal(err)
	}

	return NewPreparer(PreparerConfig{
		ConvertCacheDir: filepath.Join(dir, "converted"),
		OptimizedDir:    filepath.Join(dir, "optimized"),
		ConverterName:   convert.DefaultPreset,
		Optimize:        optimize,
		Profile:         profile,
		Syncable:        syncableFormats,
	})
}

// pdfCandidate is the committed PDF fixture, offered as a library book.
func pdfCandidate(t *testing.T) Candidate {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "text-layer.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := convert.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return Candidate{
		Path:    path,
		Format:  "pdf",
		SHA256:  sum,
		Size:    info.Size(),
		RelPath: "A. Writer/A Book",
	}
}

func TestPrepareConvertsPDFToEPUB(t *testing.T) {
	c := pdfCandidate(t)

	got, err := testPreparer(t, false).Prepare(context.Background(), []Candidate{c})
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed != 0 {
		t.Fatalf("Failed = %d, notes: %v", got.Failed, got.Notes)
	}
	if len(got.Books) != 1 {
		t.Fatalf("got %d book(s), want 1 (notes: %v)", len(got.Books), got.Notes)
	}
	b := got.Books[0]

	// A PDF cannot land at a .pdf path: the device would not render it, and
	// the path is about to be pinned.
	if !strings.HasSuffix(b.RelPath, ".epub") {
		t.Errorf("RelPath = %q, want it to end in .epub", b.RelPath)
	}
	if b.UploadPath == "" {
		t.Fatal("UploadPath is empty; nothing was converted")
	}
	if b.UploadPath == b.Path {
		t.Error("UploadPath equals Path; the PDF itself would be uploaded")
	}
}

func TestPrepareKeepsTheSourceAsIdentity(t *testing.T) {
	c := pdfCandidate(t)

	got, err := testPreparer(t, true).Prepare(context.Background(), []Candidate{c})
	if err != nil {
		t.Fatal(err)
	}
	b := got.Books[0]

	// The manifest is keyed by Path. It must stay the library file: artifact
	// paths move when the converter version or the profile changes, and a
	// moving key would make the planner see a new book and orphan a pinned one.
	if b.Path != c.Path {
		t.Errorf("Path = %q, want the library source %q", b.Path, c.Path)
	}
	if strings.Contains(b.Path, "converted") || strings.Contains(b.Path, "optimized") {
		t.Errorf("Path %q points into a cache; identity must not live there", b.Path)
	}
}

func TestPrepareHashesTheBytesActuallySent(t *testing.T) {
	c := pdfCandidate(t)

	got, err := testPreparer(t, false).Prepare(context.Background(), []Candidate{c})
	if err != nil {
		t.Fatal(err)
	}
	b := got.Books[0]

	// The manifest records the hash of what was uploaded, not of the source.
	if b.SHA256 == c.SHA256 {
		t.Error("SHA256 still describes the source PDF, not the converted EPUB")
	}
	if b.Size == c.Size {
		t.Error("Size still describes the source PDF")
	}

	wantSum, err := convert.HashFile(b.UploadPath)
	if err != nil {
		t.Fatal(err)
	}
	if b.SHA256 != wantSum {
		t.Errorf("SHA256 = %s, want the artifact's hash %s", b.SHA256, wantSum)
	}

	info, err := os.Stat(b.UploadPath)
	if err != nil {
		t.Fatal(err)
	}
	if b.Size != info.Size() {
		t.Errorf("Size = %d, want the artifact's size %d", b.Size, info.Size())
	}
}

func TestPrepareRecordsTheOptimizationProfile(t *testing.T) {
	c := pdfCandidate(t)

	got, err := testPreparer(t, true).Prepare(context.Background(), []Candidate{c})
	if err != nil {
		t.Fatal(err)
	}
	b := got.Books[0]

	if !b.Optimized {
		t.Error("Optimized is false despite optimization being enabled")
	}
	if b.OptimizeProfile != "x4-v1" {
		t.Errorf("OptimizeProfile = %q, want x4-v1", b.OptimizeProfile)
	}
}

func TestPrepareLeavesSyncableFormatsAlone(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}

	// TXT is rendered by the firmware directly, so nothing should convert it.
	// With optimization off it must pass through byte-for-byte.
	got, err := testPreparer(t, false).Prepare(context.Background(), []Candidate{{
		Path:    src,
		Format:  "txt",
		SHA256:  "abc",
		Size:    10,
		RelPath: "Someone/Notes",
	}})
	if err != nil {
		t.Fatal(err)
	}
	b := got.Books[0]

	if b.UploadPath != "" {
		t.Errorf("UploadPath = %q, want empty so the source is sent as-is", b.UploadPath)
	}
	if b.SHA256 != "abc" || b.Size != 10 {
		t.Errorf("hash/size were recomputed for an untouched file: %s/%d", b.SHA256, b.Size)
	}
	if b.RelPath != "Someone/Notes.txt" {
		t.Errorf("RelPath = %q, want Someone/Notes.txt", b.RelPath)
	}
}

func TestPrepareOptimizesAnEPUBSource(t *testing.T) {
	// Build a real EPUB by converting the fixture PDF, then offer it as an
	// EPUB the library already holds. This is the common case: most books
	// arrive as EPUB and are optimized without being converted.
	staging := t.TempDir()
	conv, err := testPreparer(t, false).Prepare(context.Background(), []Candidate{pdfCandidate(t)})
	if err != nil {
		t.Fatal(err)
	}
	epubBytes, err := os.ReadFile(conv.Books[0].UploadPath)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(staging, "book.epub")
	if err := os.WriteFile(src, epubBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	srcSum, err := convert.HashFile(src)
	if err != nil {
		t.Fatal(err)
	}

	got, err := testPreparer(t, true).Prepare(context.Background(), []Candidate{{
		Path: src, Format: "epub", SHA256: srcSum, Size: int64(len(epubBytes)),
		RelPath: "A. Writer/A Book",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed != 0 {
		t.Fatalf("Failed = %d, notes: %v", got.Failed, got.Notes)
	}
	b := got.Books[0]

	if !b.Optimized {
		t.Error("an EPUB source was not optimized")
	}
	if b.UploadPath == "" {
		t.Fatal("UploadPath is empty; the original would be sent")
	}
	if b.RelPath != "A. Writer/A Book.epub" {
		t.Errorf("RelPath = %q, want A. Writer/A Book.epub", b.RelPath)
	}
	// Identity stays the library file even though the bytes come from a cache.
	if b.Path != src {
		t.Errorf("Path = %q, want %q", b.Path, src)
	}
}

func TestPrepareDoesNotOptimizeNonEPUBs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book.txt")
	if err := os.WriteFile(src, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}

	// TXT is rendered natively by the firmware and is not a zip. Running the
	// EPUB optimizer over it fails to open the archive, and because a failed
	// book is dropped rather than fatal, every TXT would quietly stop syncing.
	got, err := testPreparer(t, true).Prepare(context.Background(), []Candidate{{
		Path: src, Format: "txt", SHA256: "abc", Size: 10, RelPath: "A/Notes",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed != 0 {
		t.Fatalf("Failed = %d with optimization on; notes: %v", got.Failed, got.Notes)
	}
	if len(got.Books) != 1 {
		t.Fatalf("got %d book(s), want the TXT to survive", len(got.Books))
	}
	b := got.Books[0]

	if b.Optimized {
		t.Error("a TXT was marked optimized")
	}
	if b.UploadPath != "" {
		t.Errorf("UploadPath = %q, want the source sent as-is", b.UploadPath)
	}
}

func TestPrepareSkipsUnrenderableFormatsWhenConversionIsOff(t *testing.T) {
	p := &Preparer{Syncable: syncableFormats} // no Convert, no Resolve

	got, err := p.Prepare(context.Background(), []Candidate{{
		Path: "/books/paper.pdf", Format: "pdf", RelPath: "X/Y",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Books) != 0 {
		t.Errorf("got %d book(s), want the PDF skipped", len(got.Books))
	}
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "cannot render") {
		t.Errorf("Notes = %v, want one explaining the skip", got.Notes)
	}
}

func TestPrepareDoesNotFailTheRunForOneBadBook(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "broken.pdf")
	if err := os.WriteFile(bad, []byte("not a pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "fine.txt")
	if err := os.WriteFile(good, []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}

	// One unconvertible PDF must not stop the rest of the library syncing.
	got, err := testPreparer(t, false).Prepare(context.Background(), []Candidate{
		{Path: bad, Format: "pdf", RelPath: "A/Bad"},
		{Path: good, Format: "txt", SHA256: "x", Size: 4, RelPath: "A/Good"},
	})
	if err != nil {
		t.Fatalf("one bad book aborted the whole preparation: %v", err)
	}
	if got.Failed != 1 {
		t.Errorf("Failed = %d, want 1", got.Failed)
	}
	if len(got.Books) != 1 {
		t.Fatalf("got %d book(s), want the good one to survive", len(got.Books))
	}
	if got.Books[0].Path != good {
		t.Errorf("surviving book is %q, want %q", got.Books[0].Path, good)
	}
}

// TestPinnedPathSurvivesAChangedArtifact is the important one.
//
// Upgrading the converter or bumping the optimizer profile moves the derived
// artifact to a new cache path and changes its hash. Neither may move a book
// that is already pinned: every move clears the firmware's .crosspoint cache
// and destroys the reading position. Only the bytes may change, in place.
func TestPinnedPathSurvivesAChangedArtifact(t *testing.T) {
	const source = "/library/A. Writer/Paper.pdf"
	pinned := "/Books/A. Writer/Paper.epub"

	manifest := &Manifest{
		Version: ManifestVersion,
		Root:    "/Books",
		Entries: []Entry{{
			SHA256:          "old-artifact-hash",
			DevicePath:      pinned,
			DeviceSize:      1000,
			LocalPath:       source,
			Optimized:       true,
			OptimizeProfile: "x4-v1",
		}},
	}

	// The same book, re-prepared after a converter upgrade: identity
	// unchanged, artifact path and hash both different.
	book := LocalBook{
		Path:            source,
		UploadPath:      "/cache/optimized/ff/newkey.epub",
		SHA256:          "new-artifact-hash",
		Size:            900,
		RelPath:         "A. Writer/Paper.epub",
		Optimized:       true,
		OptimizeProfile: "x4-v2",
	}

	plan := Build(Input{
		Root:     device.NewPath("/Books"),
		Local:    []LocalBook{book},
		Device:   map[device.Path]device.FileEntry{device.NewPath(pinned): {Size: 1000}},
		Manifest: manifest,
	})

	for _, op := range plan.Ops {
		if op.Kind == OpMove {
			t.Fatalf("a changed artifact planned a move: %s", op)
		}
	}
	if len(plan.RepathWarnings) != 0 {
		t.Errorf("RepathWarnings = %v, want none", plan.RepathWarnings)
	}

	var uploads []Op
	for _, op := range plan.Ops {
		if op.Kind == OpUpload {
			uploads = append(uploads, op)
		}
	}
	if len(uploads) != 1 {
		t.Fatalf("got %d upload(s), want exactly 1 (in place)", len(uploads))
	}
	up := uploads[0]

	if up.DevicePath != device.NewPath(pinned) {
		t.Errorf("upload went to %s, want the pinned %s", up.DevicePath, pinned)
	}
	// The bytes come from the new artifact...
	if up.LocalPath != book.UploadPath {
		t.Errorf("LocalPath = %q, want the artifact %q", up.LocalPath, book.UploadPath)
	}
	// ...but the manifest identity stays the library source.
	if up.source() != source {
		t.Errorf("source() = %q, want the library file %q", up.source(), source)
	}
}

// TestUnpinnedConvertedBookLandsAtAnEpubPath checks the other half: a book
// shelf has not placed before takes its destination from the template, and a
// converted one must arrive as an EPUB.
func TestUnpinnedConvertedBookLandsAtAnEpubPath(t *testing.T) {
	book := LocalBook{
		Path:       "/library/Paper.pdf",
		UploadPath: "/cache/converted/aa/key.epub",
		SHA256:     "h",
		Size:       10,
		RelPath:    "Paper.epub",
	}

	plan := Build(Input{
		Root:     device.NewPath("/Books"),
		Local:    []LocalBook{book},
		Device:   map[device.Path]device.FileEntry{},
		Manifest: NewManifest("x", "/Books"),
	})

	var found bool
	for _, op := range plan.Ops {
		if op.Kind != OpUpload {
			continue
		}
		found = true
		if got := op.DevicePath.String(); got != "/Books/Paper.epub" {
			t.Errorf("uploaded to %q, want /Books/Paper.epub", got)
		}
		if op.LocalPath != book.UploadPath {
			t.Errorf("LocalPath = %q, want the artifact", op.LocalPath)
		}
	}
	if !found {
		t.Fatal("no upload was planned")
	}
}

// TestManifestRecordsTheSourceNotTheArtifact closes the loop through the
// executor: what lands in the manifest has to be the identity the next run
// will look the book up by.
func TestManifestRecordsTheSourceNotTheArtifact(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "artifact.epub")
	if err := os.WriteFile(artifact, []byte("epub bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManifest("x4", "/Books")
	plan := Plan{Ops: []Op{{
		Kind:            OpUpload,
		DevicePath:      device.NewPath("/Books/Paper.epub"),
		LocalPath:       artifact,
		SourcePath:      "/library/Paper.pdf",
		Size:            10,
		SHA256:          "artifact-hash",
		Optimized:       true,
		OptimizeProfile: "x4-v1",
	}}}

	if _, err := Execute(context.Background(), newFakeTransport(), plan, m, ExecOptions{}); err != nil {
		t.Fatal(err)
	}

	if len(m.Entries) != 1 {
		t.Fatalf("got %d manifest entries, want 1", len(m.Entries))
	}
	e := m.Entries[0]

	if e.LocalPath != "/library/Paper.pdf" {
		t.Errorf("LocalPath = %q, want the library source; a cache path here breaks pinning on the next run",
			e.LocalPath)
	}
	if e.SHA256 != "artifact-hash" {
		t.Errorf("SHA256 = %q, want the hash of the bytes sent", e.SHA256)
	}
	if !e.Optimized || e.OptimizeProfile != "x4-v1" {
		t.Errorf("optimization not recorded: optimized=%v profile=%q", e.Optimized, e.OptimizeProfile)
	}

	// And the next run must find it by that identity.
	if _, ok := m.ByLocalPath()["/library/Paper.pdf"]; !ok {
		t.Error("the entry cannot be looked up by its source path")
	}
}
