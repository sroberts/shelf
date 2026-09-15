package main_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "shelf-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	binPath = filepath.Join(tmpDir, "shelf")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", binPath, "github.com/sroberts/shelf/cmd/shelf")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build shelf binary for e2e tests: %v\n%s\n", err, string(out))
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func runShelfWithEnv(t *testing.T, extraEnv []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("unexpected error running shelf: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

func runShelf(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runShelfWithEnv(t, nil, args...)
}

func TestE2EHelp(t *testing.T) {
	stdout, _, code := runShelf(t, "--help")
	if code != 0 {
		t.Errorf("shelf --help exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "usage: shelf") {
		t.Errorf("shelf --help output missing usage string: %q", stdout)
	}
	if !strings.Contains(stdout, "scan") || !strings.Contains(stdout, "ls") {
		t.Errorf("shelf --help output missing core commands: %q", stdout)
	}
}

func TestE2EVersion(t *testing.T) {
	stdout, _, code := runShelf(t, "version")
	if code != 0 {
		t.Errorf("shelf version exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(stdout, "shelf ") {
		t.Errorf("shelf version output unexpected: %q", stdout)
	}
}

func TestE2EUnknownCommand(t *testing.T) {
	_, stderr, code := runShelf(t, "nonexistent-cmd")
	if code != 2 {
		t.Errorf("shelf nonexistent-cmd exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("shelf nonexistent-cmd stderr missing 'unknown command': %q", stderr)
	}
}

func TestE2ENoTUIWithoutArgs(t *testing.T) {
	stdout, _, code := runShelf(t, "--no-tui")
	if code != 2 {
		t.Errorf("shelf --no-tui exit code = %d, want 2 (usage)", code)
	}
	if !strings.Contains(stdout, "usage: shelf") {
		t.Errorf("shelf --no-tui stdout missing usage: %q", stdout)
	}
}

func createTestEPUB(t *testing.T, path, title, author string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	mt := []byte("application/epub+zip")
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: "mimetype", Method: zip.Store, CRC32: crc32.ChecksumIEEE(mt),
		CompressedSize64: uint64(len(mt)), UncompressedSize64: uint64(len(mt)),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(mt)

	container := `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`

	opf := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>%s</dc:title>
    <dc:creator>%s</dc:creator>
    <dc:identifier id="uid">test-%s</dc:identifier>
    <dc:language>en</dc:language>
  </metadata>
  <manifest><item id="c1" href="c1.html" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="c1"/></spine>
</package>`, title, author, title)

	for name, body := range map[string]string{
		"META-INF/container.xml": container,
		"OEBPS/content.opf":      opf,
		"OEBPS/c1.html":          "<html><body><p>Test content</p></body></html>",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestE2ELibraryLifecycle(t *testing.T) {
	testDir := t.TempDir()
	libDir := filepath.Join(testDir, "books")
	env := []string{
		"XDG_DATA_HOME=" + filepath.Join(testDir, "data"),
		"XDG_CONFIG_HOME=" + filepath.Join(testDir, "config"),
		"XDG_STATE_HOME=" + filepath.Join(testDir, "state"),
		"XDG_CACHE_HOME=" + filepath.Join(testDir, "cache"),
	}

	epubPath := filepath.Join(libDir, "Le Guin", "Earthsea.epub")
	createTestEPUB(t, epubPath, "A Wizard of Earthsea", "Ursula K. Le Guin")

	// 1. Scan the library directory
	stdout, stderr, code := runShelfWithEnv(t, env, "--library", libDir, "scan", libDir)
	if code != 0 {
		t.Fatalf("shelf scan failed with code %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// 2. Run shelf ls
	stdout, stderr, code = runShelfWithEnv(t, env, "--library", libDir, "ls")
	if code != 0 {
		t.Fatalf("shelf ls failed with code %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Earthsea") {
		t.Errorf("shelf ls output missing 'Earthsea': %q", stdout)
	}

	// 3. Run shelf ls --json (emits newline-delimited JSON)
	stdout, stderr, code = runShelfWithEnv(t, env, "--library", libDir, "ls", "--json")
	if code != 0 {
		t.Fatalf("shelf ls --json failed with code %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line of JSON, got %d: %q", len(lines), stdout)
	}
	var book map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &book); err != nil {
		t.Fatalf("shelf ls --json output is not valid JSON: %v\nOutput: %s", err, lines[0])
	}
	if title, ok := book["title"].(string); !ok || title != "A Wizard of Earthsea" {
		t.Errorf("expected title 'A Wizard of Earthsea', got %v", book["title"])
	}

	// 4. Run shelf tags --json
	stdout, stderr, code = runShelfWithEnv(t, env, "--library", libDir, "tags", "--json")
	if code != 0 {
		t.Fatalf("shelf tags --json failed with code %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// 5. Run shelf doctor --offline
	stdout, stderr, code = runShelfWithEnv(t, env, "--library", libDir, "doctor", "--offline")
	if code != 0 {
		t.Fatalf("shelf doctor failed with code %d:\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "offline") && !strings.Contains(stdout, "pass") && !strings.Contains(stdout, "ok") {
		t.Logf("shelf doctor output: %s", stdout)
	}
}

func TestE2ERm(t *testing.T) {
	testDir := t.TempDir()
	libDir := filepath.Join(testDir, "books")
	env := []string{
		"XDG_DATA_HOME=" + filepath.Join(testDir, "data"),
		"XDG_CONFIG_HOME=" + filepath.Join(testDir, "config"),
		"XDG_STATE_HOME=" + filepath.Join(testDir, "state"),
		"XDG_CACHE_HOME=" + filepath.Join(testDir, "cache"),
	}

	authorDir := filepath.Join(libDir, "Le Guin")
	epubPath := filepath.Join(authorDir, "Earthsea.epub")
	createTestEPUB(t, epubPath, "A Wizard of Earthsea", "Ursula K. Le Guin")

	// 1. Scan the library
	_, _, code := runShelfWithEnv(t, env, "--library", libDir, "scan", libDir)
	if code != 0 {
		t.Fatalf("scan failed: code %d", code)
	}

	// 2. Dry run rm
	stdout, _, code := runShelfWithEnv(t, env, "--library", libDir, "rm", "--dry-run", "Earthsea")
	if code != 0 {
		t.Fatalf("rm --dry-run failed with code %d", code)
	}
	if !strings.Contains(stdout, "would delete") {
		t.Errorf("expected 'would delete' in stdout, got %q", stdout)
	}
	if _, err := os.Stat(epubPath); err != nil {
		t.Fatalf("dry-run deleted file: %v", err)
	}

	// 3. rm without --yes in non-interactive mode
	_, stderr, code := runShelfWithEnv(t, env, "--library", libDir, "rm", "Earthsea")
	if code == 0 {
		t.Fatalf("expected non-zero exit code when prompting non-interactively without --yes")
	}
	if !strings.Contains(stderr, "refusing to prompt non-interactively; pass --yes") {
		t.Errorf("expected refusal message in stderr, got: %q", stderr)
	}
	if _, err := os.Stat(epubPath); err != nil {
		t.Fatalf("unconfirmed rm deleted file: %v", err)
	}

	// 4. rm with --yes
	stdout, _, code = runShelfWithEnv(t, env, "--library", libDir, "rm", "--yes", "Earthsea")
	if code != 0 {
		t.Fatalf("rm --yes failed with code %d", code)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Errorf("expected 'deleted' in stdout, got %q", stdout)
	}

	// File should be deleted
	if _, err := os.Stat(epubPath); !os.IsNotExist(err) {
		t.Errorf("file still exists after rm --yes: %v", err)
	}

	// Empty parent directory should be pruned
	if _, err := os.Stat(authorDir); !os.IsNotExist(err) {
		t.Errorf("authorDir was not pruned: %v", err)
	}

	// Library root should still exist
	if _, err := os.Stat(libDir); err != nil {
		t.Errorf("library root was deleted: %v", err)
	}

	// shelf ls should now find no books
	_, stderr, code = runShelfWithEnv(t, env, "--library", libDir, "ls")
	if code != 0 {
		t.Errorf("shelf ls exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr, "no books match") {
		t.Errorf("expected 'no books match' after deletion, got: %q", stderr)
	}

	// 5. Test shelf delete alias and multi-book deletion
	epub1 := filepath.Join(libDir, "Author1", "Book1.epub")
	epub2 := filepath.Join(libDir, "Author2", "Book2.epub")
	createTestEPUB(t, epub1, "Book One", "Author 1")
	createTestEPUB(t, epub2, "Book Two", "Author 2")

	_, _, code = runShelfWithEnv(t, env, "--library", libDir, "scan", libDir)
	if code != 0 {
		t.Fatalf("second scan failed: code %d", code)
	}

	stdout, _, code = runShelfWithEnv(t, env, "--library", libDir, "delete", "-y", epub1, epub2)
	if code != 0 {
		t.Fatalf("delete -y failed with code %d", code)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Errorf("expected 'deleted' in stdout, got %q", stdout)
	}
	if _, err := os.Stat(epub1); !os.IsNotExist(err) {
		t.Errorf("epub1 still exists: %v", err)
	}
	if _, err := os.Stat(epub2); !os.IsNotExist(err) {
		t.Errorf("epub2 still exists: %v", err)
	}
}
