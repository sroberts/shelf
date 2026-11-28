package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sroberts/shelf/internal/ai"
	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
)

// parityLibrary is an isolated shelf with two indexed books, and the env and
// library flag every command in these tests runs with.
func parityLibrary(t *testing.T) (dir, lib string, env []string) {
	t.Helper()
	dir = t.TempDir()
	lib = filepath.Join(dir, "books")
	env = append(isolatedEnv(dir), "OUTPOST_STORAGE_DIR=")
	createTestEPUB(t, filepath.Join(lib, "Le Guin", "Earthsea.epub"), "A Wizard of Earthsea", "Ursula K. Le Guin")
	createTestEPUB(t, filepath.Join(lib, "Herbert", "Dune.epub"), "Dune", "Frank Herbert")
	if out, errOut, code := runShelfWithEnv(t, env, "--library", lib, "scan", lib); code != 0 {
		t.Fatalf("scan: %d\n%s\n%s", code, out, errOut)
	}
	return dir, lib, env
}

func indexedBook(t *testing.T, dir, title string) *library.Book {
	t.Helper()
	db, err := library.Open(filepath.Join(dir, "data", "shelf", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	books, err := db.Search(title, library.SearchOptions{Limit: 2})
	if err != nil || len(books) != 1 {
		t.Fatalf("search %q: %v, %d books", title, err, len(books))
	}
	return books[0]
}

func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("not JSON: %q: %v", line, err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestE2EGenres(t *testing.T) {
	dir, lib, env := parityLibrary(t)
	aiFile := filepath.Join(dir, "data", "shelf", "ai.db")

	// Before shelf serve has classified anything, genres says why it is empty
	// and nothing creates ai.db as a side effect.
	_, errOut, code := runShelfWithEnv(t, env, "--library", lib, "genres")
	if code != 0 || !strings.Contains(errOut, "shelf serve") {
		t.Errorf("genres with no store: code %d, stderr %q", code, errOut)
	}
	runShelfWithEnv(t, env, "--library", lib, "ls", "--genre", "fantasy")
	if _, err := os.Stat(aiFile); !os.IsNotExist(err) {
		t.Fatalf("a CLI command created ai.db: %v", err)
	}

	store, err := ai.OpenStore(aiFile)
	if err != nil {
		t.Fatal(err)
	}
	earthsea := indexedBook(t, dir, "Earthsea")
	if err := store.SetGenre(earthsea.SHA256, "fantasy", nil, "test"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	out, errOut, code := runShelfWithEnv(t, env, "--library", lib, "ls", "--json", "--genre", "Fantasy")
	if code != 0 {
		t.Fatalf("ls --genre: %d %s", code, errOut)
	}
	rows := jsonLines(t, out)
	if len(rows) != 1 || rows[0]["title"] != "A Wizard of Earthsea" || rows[0]["genre"] != "fantasy" {
		t.Errorf("ls --genre fantasy = %v", rows)
	}

	out, _, _ = runShelfWithEnv(t, env, "--library", lib, "ls")
	if !strings.Contains(out, "GENRE") || !strings.Contains(out, "fantasy") {
		t.Errorf("ls table lacks the genre column:\n%s", out)
	}

	out, _, _ = runShelfWithEnv(t, env, "--library", lib, "genres", "--json")
	if rows := jsonLines(t, out); len(rows) != 1 || rows[0]["genre"] != "fantasy" || rows[0]["count"] != 1.0 {
		t.Errorf("genres --json = %v", rows)
	}

	out, _, _ = runShelfWithEnv(t, env, "--library", lib, "meta", "earthsea")
	if !strings.Contains(out, "genre") || !strings.Contains(out, "fantasy") {
		t.Errorf("meta does not show the genre:\n%s", out)
	}
}

func TestE2EProgress(t *testing.T) {
	dir, lib, env := parityLibrary(t)

	// No user anywhere is a usage error that says how to name one.
	_, errOut, code := runShelfWithEnv(t, env, "--library", lib, "progress")
	if code != 2 || !strings.Contains(errOut, "--user") {
		t.Errorf("progress with no user: code %d, stderr %q", code, errOut)
	}

	dune := indexedBook(t, dir, "Dune")
	store, err := kosync.OpenStore(filepath.Join(dir, "data", "shelf", "progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutProgress("reader", kosync.Progress{Document: dune.DocID, Percentage: 0.42, Device: "X4"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	out, errOut, code := runShelfWithEnv(t, env, "--library", lib, "progress", "--user", "reader", "--json")
	if code != 0 {
		t.Fatalf("progress --json: %d %s", code, errOut)
	}
	rows := jsonLines(t, out)
	if len(rows) != 1 || rows[0]["device"] != "X4" || rows[0]["percentage"] != 42.0 {
		t.Fatalf("progress --json = %v", rows)
	}
	if b, _ := rows[0]["book"].(map[string]any); b["title"] != "Dune" {
		t.Errorf("progress entry book = %v", rows[0]["book"])
	}

	out, _, _ = runShelfWithEnv(t, env, "--library", lib, "progress", "--user", "reader")
	if !strings.Contains(out, "Dune") || !strings.Contains(out, "42%") || !strings.Contains(out, "X4") {
		t.Errorf("progress table:\n%s", out)
	}

	// Clearing without --yes and without a terminal refuses rather than
	// guessing.
	_, _, code = runShelfWithEnv(t, env, "--library", lib, "progress", "clear", "dune", "--user", "reader")
	if code == 0 {
		t.Error("progress clear without --yes succeeded non-interactively")
	}

	out, errOut, code = runShelfWithEnv(t, env, "--library", lib, "progress", "clear", "dune", "--user", "reader", "--yes")
	if code != 0 || !strings.Contains(out, "cleared Dune") {
		t.Fatalf("progress clear: %d\n%s\n%s", code, out, errOut)
	}
	out, _, _ = runShelfWithEnv(t, env, "--library", lib, "progress", "--user", "reader", "--json")
	if strings.TrimSpace(out) != "" {
		t.Errorf("progress after clearing = %q", out)
	}

	// A second clear has nothing to do, and says so with a failure.
	_, errOut, code = runShelfWithEnv(t, env, "--library", lib, "progress", "clear", "dune", "--user", "reader", "--yes")
	if code != 1 || !strings.Contains(errOut, "nothing to clear") {
		t.Errorf("second clear: code %d, stderr %q", code, errOut)
	}
}

func TestE2EStats(t *testing.T) {
	_, lib, env := parityLibrary(t)
	out, errOut, code := runShelfWithEnv(t, env, "--library", lib, "stats", "--json")
	if code != 0 {
		t.Fatalf("stats: %d %s", code, errOut)
	}
	rows := jsonLines(t, out)
	if len(rows) != 1 || rows[0]["books"] != 2.0 || rows[0]["authors"] != 2.0 {
		t.Errorf("stats --json = %v", rows)
	}
	out, _, _ = runShelfWithEnv(t, env, "--library", lib, "stats")
	if !strings.Contains(out, "books") || !strings.Contains(out, "epub 2") {
		t.Errorf("stats table:\n%s", out)
	}
}

func TestE2EMailStatus(t *testing.T) {
	dir, lib, env := parityLibrary(t)
	createTestEPUB(t, filepath.Join(lib, "Mail", "A Newsletter", "2026-01-01 Issue 1.epub"), "Issue 1", "A Newsletter")
	runShelfWithEnv(t, env, "--library", lib, "scan", lib)
	if err := os.WriteFile(filepath.Join(dir, "data", "shelf", "mail.json"), []byte(`{"after":7}`), 0o600); err != nil {
		t.Fatal(err)
	}

	env = append(env, "OUTPOST_MAIL_URL=", "OUTPOST_MAIL_TOKEN=")
	out, errOut, code := runShelfWithEnv(t, env, "--library", lib, "mail", "--json")
	if code != 0 {
		t.Fatalf("mail: %d %s", code, errOut)
	}
	rows := jsonLines(t, out)
	if len(rows) != 1 || rows[0]["books"] != 1.0 || rows[0]["cursor"] != 7.0 || rows[0]["configured"] != false {
		t.Errorf("mail --json = %v", rows)
	}
	out, _, _ = runShelfWithEnv(t, env, "--library", lib, "mail")
	if !strings.Contains(out, "OUTPOST_MAIL_URL") {
		t.Errorf("mail does not explain that import is off:\n%s", out)
	}
}

func TestE2ESkill(t *testing.T) {
	out, _, code := runShelf(t, "skill", "show")
	if code != 0 || !strings.Contains(out, "name: shelf") || !strings.Contains(out, "--repath") {
		t.Fatalf("skill show: code %d\n%s", code, out)
	}

	skills := t.TempDir()
	dest := filepath.Join(skills, "shelf", "SKILL.md")
	if out, errOut, code := runShelf(t, "skill", "install", "--dir", skills); code != 0 {
		t.Fatalf("install: %d\n%s\n%s", code, out, errOut)
	}
	data, err := os.ReadFile(dest)
	if err != nil || !strings.Contains(string(data), "name: shelf") {
		t.Fatalf("installed skill: %v\n%s", err, data)
	}

	out, _, code = runShelf(t, "skill", "install", "--dir", skills)
	if code != 0 || !strings.Contains(out, "up to date") {
		t.Errorf("reinstall: code %d, %q", code, out)
	}

	// A skill someone edited is not overwritten without --force.
	if err := os.WriteFile(dest, []byte("my own notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := runShelf(t, "skill", "install", "--dir", skills)
	if code != 1 || !strings.Contains(errOut, "--force") {
		t.Errorf("install over an edited skill: code %d, %q", code, errOut)
	}
	if data, _ := os.ReadFile(dest); string(data) != "my own notes\n" {
		t.Error("the edited skill was overwritten")
	}
	if _, _, code := runShelf(t, "skill", "install", "--dir", skills, "--force"); code != 0 {
		t.Errorf("install --force: code %d", code)
	}
}

func TestE2EDevicesAdd(t *testing.T) {
	dir := t.TempDir()
	env := append(isolatedEnv(dir), "OUTPOST_STORAGE_DIR=")

	_, _, code := runShelfWithEnv(t, env, "devices", "add", "reader")
	if code != 2 {
		t.Errorf("devices add without --host: code %d, want 2", code)
	}

	out, errOut, code := runShelfWithEnv(t, env, "devices", "add", "reader", "--host", "10.0.0.9", "--root", "Books")
	if code != 0 {
		t.Fatalf("devices add: %d\n%s\n%s", code, out, errOut)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config", "shelf", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`nickname = "reader"`, `host = "10.0.0.9"`, `root = "/Books"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config.toml lacks %s:\n%s", want, data)
		}
	}

	_, errOut, code = runShelfWithEnv(t, env, "devices", "add", "reader", "--host", "10.0.0.10")
	if code == 0 {
		t.Errorf("a duplicate nickname was accepted: %s", errOut)
	}

	out, _, _ = runShelfWithEnv(t, env, "devices", "--json")
	if !strings.Contains(out, "reader") {
		t.Errorf("devices --json does not list the new device: %s", out)
	}
}

// A generated password is printed once, signs in, and comes with the address
// the reader needs when one is given (a Funnel URL).
func TestE2EUserAddGenerate(t *testing.T) {
	dir, _, env := parityLibrary(t)

	out, errOut, code := runShelfWithEnv(t, env, "user", "add", "--generate", "--url", "https://shelf.example.ts.net/", "x4")
	if code != 0 {
		t.Fatalf("user add --generate: %d\n%s\n%s", code, out, errOut)
	}
	m := regexp.MustCompile(`password:\s+([a-z0-9]{4}(?:-[a-z0-9]{4}){3})`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no generated password in:\n%s", out)
	}
	for _, want := range []string{"https://shelf.example.ts.net/opds", "sync server: https://shelf.example.ts.net\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	store, err := kosync.OpenStore(filepath.Join(dir, "data", "shelf", "progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CheckPassword("x4", m[1]); err != nil {
		t.Errorf("printed password does not sign in: %v", err)
	}

	if _, _, code := runShelfWithEnv(t, env, "user", "passwd", "--generate", "--password", "abcdefghijk", "x4"); code != 2 {
		t.Errorf("--generate with --password: code %d, want a usage error", code)
	}
}
