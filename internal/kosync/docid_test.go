package kosync

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestOffsetsMatchKOReader pins the sample offsets.
//
// This is the highest-stakes assertion in the package. A wrong offset produces
// no error and no warning; it produces a hash that simply never matches the
// device, so reading progress silently never syncs and the cause is invisible.
//
// The expected values were derived twice, independently:
//
//   - From KOReader's frontend/util.lua, which computes lshift(1024, 2*i) for
//     i = -1..10 using LuaJIT's bit library. That library masks the shift count
//     to five bits, so i = -1 gives lshift(1024, 30), which overflows 32 bits
//     to 0.
//   - From CrossPoint's lib/KOReaderSync/KOReaderDocumentId.cpp, whose
//     getOffset returns 0 for i < 0 and 1024 << 2i otherwise.
//
// Note that CrossPoint's own header comment contradicts its code, listing the
// first offset as 256. The code is right. If this test is ever "fixed" to
// expect 256 because the documentation says so, sync will break with no
// symptom other than progress never appearing.
func TestOffsetsMatchKOReader(t *testing.T) {
	want := []int64{
		0, // i = -1: lshift(1024, -2) wraps to 0, it is NOT 256
		1024,
		4096,
		16384,
		65536,
		262144,
		1048576,
		4194304,
		16777216,
		67108864,
		268435456,
		1073741824,
	}

	got := Offsets()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("offsets do not match KOReader:\n got %v\nwant %v", got, want)
	}
	if len(got) != sampleCount {
		t.Errorf("got %d offsets, want %d", len(got), sampleCount)
	}
	if got[0] != 0 {
		t.Error("the first offset must be 0; 256 is what the CrossPoint header " +
			"comment says and it is wrong")
	}
}

// A small file is hashed from the bytes that exist, stopping at the first
// offset past its end.
func TestDocumentIDShortFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.epub")

	body := bytes.Repeat([]byte("A"), 500) // shorter than one chunk
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DocumentID(path)
	if err != nil {
		t.Fatal(err)
	}

	// Only offset 0 lands inside the file, so the hash is MD5 of the whole file.
	want := md5.Sum(body)
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("DocumentID = %s, want MD5 of the whole 500-byte file %s",
			got, hex.EncodeToString(want[:]))
	}
}

// A file spanning several offsets hashes the concatenated samples, in order.
func TestDocumentIDSamplesInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.epub")

	// 20 KiB reaches offsets 0, 1024, 4096, and 16384.
	size := 20 * 1024
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i % 251) // non-repeating enough that order matters
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DocumentID(path)
	if err != nil {
		t.Fatal(err)
	}

	// Recompute independently from the offset list.
	h := md5.New()
	var used []int64
	for _, off := range Offsets() {
		if off >= int64(size) {
			break
		}
		end := off + chunkSize
		if end > int64(size) {
			end = int64(size)
		}
		h.Write(body[off:end])
		used = append(used, off)
	}
	want := hex.EncodeToString(h.Sum(nil))

	if got != want {
		t.Errorf("DocumentID = %s, want %s (sampled offsets %v)", got, want, used)
	}
	if len(used) != 4 {
		t.Errorf("sampled %d offsets, want 4 for a 20 KiB file", len(used))
	}
}

// Content decides identity, not the name: the same bytes under two names hash
// alike, and different bytes under one name do not.
func TestDocumentIDDependsOnContentNotName(t *testing.T) {
	dir := t.TempDir()

	body := bytes.Repeat([]byte("Call me Ishmael. "), 200)
	a := filepath.Join(dir, "moby.epub")
	b := filepath.Join(dir, "completely-different-name.epub")
	os.WriteFile(a, body, 0o644)
	os.WriteFile(b, body, 0o644)

	ha, err := DocumentID(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := DocumentID(b)
	if ha != hb {
		t.Error("the same bytes under different names produced different ids")
	}

	// Change a byte inside a sampled window, not past the end of one.
	changed := make([]byte, len(body))
	copy(changed, body)
	changed[10] = '!'
	c := filepath.Join(dir, "other.epub")
	os.WriteFile(c, changed, 0o644)
	hc, _ := DocumentID(c)
	if ha == hc {
		t.Error("changed content inside a sampled window produced the same id")
	}
}

// Appending to a file need not change its id, and that is not a bug.
//
// The algorithm samples fixed windows and stops at the first offset past EOF,
// so bytes added beyond the last window are never read. Worth pinning: it is
// surprising, it is inherent to KOReader's design, and a future "improvement"
// that made the id sensitive to length would silently stop matching every
// device in the world.
func TestDocumentIDCanIgnoreAppendedBytes(t *testing.T) {
	dir := t.TempDir()

	// 3400 bytes samples offsets 0 and 1024 only; 4096 is past the end, so
	// everything from 2048 onwards is invisible.
	body := bytes.Repeat([]byte("Call me Ishmael. "), 200)
	a := filepath.Join(dir, "a.epub")
	os.WriteFile(a, body, 0o644)

	b := filepath.Join(dir, "b.epub")
	os.WriteFile(b, append(append([]byte(nil), body...), 'x'), 0o644)

	ha, _ := DocumentID(a)
	hb, _ := DocumentID(b)
	if ha != hb {
		t.Errorf("appending past the last sampled window changed the id:\n %s\n %s", ha, hb)
	}
}

// Only the first 1024 bytes of each sample window are read, so a change in the
// gap between windows is invisible. That is inherent to the algorithm and worth
// pinning so nobody "fixes" it into a full-file hash.
func TestDocumentIDIgnoresBytesBetweenSamples(t *testing.T) {
	dir := t.TempDir()

	size := 8 * 1024
	body := make([]byte, size)
	a := filepath.Join(dir, "a.epub")
	os.WriteFile(a, body, 0o644)

	// Byte 3000 sits between the windows at 1024..2048 and 4096..5120.
	modified := make([]byte, size)
	copy(modified, body)
	modified[3000] = 0xFF
	b := filepath.Join(dir, "b.epub")
	os.WriteFile(b, modified, 0o644)

	ha, _ := DocumentID(a)
	hb, _ := DocumentID(b)
	if ha != hb {
		t.Error("a byte between sample windows changed the id; " +
			"the algorithm samples windows, it does not hash the whole file")
	}
}

func TestDocumentIDIsStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.epub")
	os.WriteFile(path, bytes.Repeat([]byte("x"), 5000), 0o644)

	first, err := DocumentID(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := DocumentID(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("id changed between runs: %s then %s", first, got)
		}
	}
	if len(first) != 32 {
		t.Errorf("id is %d hex chars, want 32", len(first))
	}
}

func TestDocumentIDMissingFile(t *testing.T) {
	if _, err := DocumentID(filepath.Join(t.TempDir(), "nope.epub")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestDocumentIDEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.epub")
	os.WriteFile(path, nil, 0o644)

	got, err := DocumentID(path)
	if err != nil {
		t.Fatal(err)
	}
	// No sample lands inside a zero-byte file, so this is MD5 of nothing.
	empty := md5.Sum(nil)
	if got != hex.EncodeToString(empty[:]) {
		t.Errorf("empty file id = %s, want MD5 of no bytes", got)
	}
}

func TestFilenameID(t *testing.T) {
	// The device hashes the bare filename, so the directory must not matter.
	a := FilenameID("/Books/Melville/Moby-Dick.epub")
	b := FilenameID("/completely/other/path/Moby-Dick.epub")
	if a != b {
		t.Error("FilenameID depends on the directory; it must use the base name only")
	}

	want := md5.Sum([]byte("Moby-Dick.epub"))
	if a != hex.EncodeToString(want[:]) {
		t.Errorf("FilenameID = %s, want MD5 of the base name", a)
	}

	if FilenameID("/Books/a.epub") == FilenameID("/Books/b.epub") {
		t.Error("different filenames produced the same id")
	}
}

func TestIDFor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.epub")
	os.WriteFile(path, bytes.Repeat([]byte("z"), 4000), 0o644)

	binary, err := IDFor(path, MatchBinary)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := DocumentID(path); binary != want {
		t.Error("MatchBinary did not use the content hash")
	}

	// An empty method defaults to binary, matching the device's default.
	if def, err := IDFor(path, ""); err != nil || def != binary {
		t.Errorf("empty method = %q, %v; want the binary hash", def, err)
	}

	name, err := IDFor(path, MatchFilename)
	if err != nil {
		t.Fatal(err)
	}
	if name != FilenameID(path) {
		t.Error("MatchFilename did not use the filename hash")
	}
	if name == binary {
		t.Error("the two methods produced the same id")
	}

	if _, err := IDFor(path, "sha256"); err == nil {
		t.Error("expected an error for an unknown match method")
	}
}

// The protocol sends an MD5 of the password rather than the password. Weak, but
// it is what the clients send and shelf does not get to redefine it.
func TestPasswordKey(t *testing.T) {
	got := PasswordKey("hunter2")
	want := md5.Sum([]byte("hunter2"))
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("PasswordKey = %s, want %s", got, hex.EncodeToString(want[:]))
	}
	if len(got) != 32 {
		t.Errorf("key is %d chars, want 32", len(got))
	}
	if PasswordKey("a") == PasswordKey("b") {
		t.Error("different passwords produced the same key")
	}
}

func TestNormalizeUsername(t *testing.T) {
	if NormalizeUsername("  scott  ") != "scott" {
		t.Error("surrounding space should be trimmed")
	}
	// Case is preserved: the protocol says nothing about folding it, and
	// guessing could merge two distinct accounts.
	if NormalizeUsername("Scott") == NormalizeUsername("scott") {
		t.Error("usernames should not be case-folded")
	}
}
