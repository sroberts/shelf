package kosync

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "kosync.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSetPasswordChangesTheCredential(t *testing.T) {
	store := newStore(t)

	if err := store.CreateUser("reader", PasswordKey("old")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetPassword("reader", PasswordKey("new")); err != nil {
		t.Fatalf("set password: %v", err)
	}

	if err := store.Authenticate("reader", PasswordKey("new")); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
	if err := store.Authenticate("reader", PasswordKey("old")); !errors.Is(err, ErrBadPassword) {
		t.Errorf("old password still works, got %v", err)
	}
}

// A fresh salt on every change is what keeps two accounts that share a password
// from storing the same verifier, so it is worth asserting rather than assuming.
func TestSetPasswordRerollsTheSalt(t *testing.T) {
	store := newStore(t)

	if err := store.CreateUser("reader", PasswordKey("secret")); err != nil {
		t.Fatalf("create: %v", err)
	}
	var before string
	if err := store.db.QueryRow(`SELECT salt FROM users WHERE username = ?`, "reader").
		Scan(&before); err != nil {
		t.Fatalf("read salt: %v", err)
	}

	// Same password, so only a new salt can change the stored row.
	if err := store.SetPassword("reader", PasswordKey("secret")); err != nil {
		t.Fatalf("set password: %v", err)
	}
	var after string
	if err := store.db.QueryRow(`SELECT salt FROM users WHERE username = ?`, "reader").
		Scan(&after); err != nil {
		t.Fatalf("read salt: %v", err)
	}

	if before == after {
		t.Error("salt was reused across a password change")
	}
	if err := store.Authenticate("reader", PasswordKey("secret")); err != nil {
		t.Errorf("authenticate after re-salt: %v", err)
	}
}

func TestSetPasswordUnknownUser(t *testing.T) {
	store := newStore(t)
	if err := store.SetPassword("nobody", PasswordKey("x")); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("got %v, want ErrNoSuchUser", err)
	}
}

// Usernames are case-sensitive, as in kosync itself; normalization is only
// whitespace trimming. Surrounding spaces are what a device keyboard adds by
// accident, and are the one difference that must not create a second account.
func TestUsernameNormalizationIsWhitespaceOnly(t *testing.T) {
	store := newStore(t)
	if err := store.CreateUser("  reader  ", PasswordKey("old")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetPassword("reader ", PasswordKey("new")); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if err := store.Authenticate("reader", PasswordKey("new")); err != nil {
		t.Errorf("authenticate: %v", err)
	}

	// A different case is a different account.
	if err := store.SetPassword("Reader", PasswordKey("new")); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("case-folded username matched, got %v", err)
	}
}

// Progress is keyed by username, so leaving it behind would let a later account
// of the same name inherit a stranger's reading positions.
func TestDeleteUserTakesProgressWithIt(t *testing.T) {
	store := newStore(t)

	if err := store.CreateUser("reader", PasswordKey("pw")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.PutProgress("reader", Progress{
		Document: "abc123", Progress: "/body/DocFragment[3]", Percentage: 0.42,
	}); err != nil {
		t.Fatalf("put progress: %v", err)
	}

	if err := store.DeleteUser("reader"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := store.CreateUser("reader", PasswordKey("pw")); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, err := store.GetProgress("reader", "abc123"); !errors.Is(err, ErrNoProgress) {
		t.Errorf("recreated account inherited progress, got %v", err)
	}
}

func TestDeleteUserLeavesOtherAccountsAlone(t *testing.T) {
	store := newStore(t)

	for _, name := range []string{"alice", "bob"} {
		if err := store.CreateUser(name, PasswordKey("pw")); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := store.PutProgress(name, Progress{Document: "doc", Percentage: 0.5}); err != nil {
			t.Fatalf("put progress %s: %v", name, err)
		}
	}

	if err := store.DeleteUser("alice"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := store.Authenticate("bob", PasswordKey("pw")); err != nil {
		t.Errorf("bob's account was affected: %v", err)
	}
	if _, err := store.GetProgress("bob", "doc"); err != nil {
		t.Errorf("bob's progress was affected: %v", err)
	}
}

func TestDeleteUserUnknown(t *testing.T) {
	store := newStore(t)
	if err := store.DeleteUser("nobody"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("got %v, want ErrNoSuchUser", err)
	}
}

func TestUserList(t *testing.T) {
	store := newStore(t)

	if got, err := store.UserList(); err != nil || len(got) != 0 {
		t.Fatalf("empty store: got %v, %v", got, err)
	}

	// Created out of order; UserList sorts by name.
	for _, name := range []string{"zoe", "alice"} {
		if err := store.CreateUser(name, PasswordKey("pw")); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	for _, doc := range []string{"one", "two", "three"} {
		if _, err := store.PutProgress("zoe", Progress{Document: doc, Percentage: 0.1}); err != nil {
			t.Fatalf("put progress: %v", err)
		}
	}

	users, err := store.UserList()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}
	if users[0].Username != "alice" || users[1].Username != "zoe" {
		t.Errorf("not sorted by username: %v", users)
	}
	if users[0].Books != 0 {
		t.Errorf("alice: got %d books, want 0", users[0].Books)
	}
	if users[1].Books != 3 {
		t.Errorf("zoe: got %d books, want 3", users[1].Books)
	}
	if users[0].CreatedAt == 0 {
		t.Error("created_at not populated")
	}
}

// The whole point of managing accounts from the CLI is that it works while the
// server is running, which means a second connection to the same file.
func TestStoreToleratesASecondConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kosync.db")

	serving, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	defer serving.Close()

	cli, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer cli.Close()

	if err := cli.CreateUser("reader", PasswordKey("pw")); err != nil {
		t.Fatalf("create through second connection: %v", err)
	}
	if err := serving.Authenticate("reader", PasswordKey("pw")); err != nil {
		t.Errorf("first connection did not see the new account: %v", err)
	}
}

func TestDeleteProgressForgetsOneDocument(t *testing.T) {
	store := newStore(t)
	for _, doc := range []string{"keep", "drop"} {
		if _, err := store.PutProgress("reader", Progress{Document: doc, Percentage: 0.3}); err != nil {
			t.Fatalf("put %s: %v", doc, err)
		}
	}

	// The username is normalised (trimmed) like every other store call.
	if err := store.DeleteProgress(" reader ", "drop"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetProgress("reader", "drop"); !errors.Is(err, ErrNoProgress) {
		t.Errorf("dropped document still has progress: %v", err)
	}
	if _, err := store.GetProgress("reader", "keep"); err != nil {
		t.Errorf("other document lost its progress: %v", err)
	}
	if err := store.DeleteProgress("reader", "drop"); !errors.Is(err, ErrNoProgress) {
		t.Errorf("second delete = %v, want ErrNoProgress", err)
	}
}

func TestGeneratedPasswordsAreTypeableAndDistinct(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		p := GeneratePassword()
		if len(p) != 19 || strings.Count(p, "-") != 3 {
			t.Fatalf("shape of %q", p)
		}
		for _, c := range strings.ReplaceAll(p, "-", "") {
			if !strings.ContainsRune(passwordAlphabet, c) {
				t.Fatalf("%q has %q, outside the alphabet", p, c)
			}
		}
		if seen[p] {
			t.Fatalf("repeated %q", p)
		}
		seen[p] = true
	}
}

func TestLimiterRefusesAnAddressNotAnAccount(t *testing.T) {
	l := NewLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	from := func(remote, fwd string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if fwd != "" {
			r.Header.Set("X-Forwarded-For", fwd)
		}
		return r
	}
	attacker := from("127.0.0.1:5000", "203.0.113.9")
	reader := from("127.0.0.1:5001", "198.51.100.4")

	for range l.Max {
		if !l.Allowed(attacker) {
			t.Fatal("refused before the limit")
		}
		l.Failed(attacker)
	}
	if l.Allowed(attacker) {
		t.Error("allowed past the limit")
	}
	// Another address, through the same proxy, is unaffected.
	if !l.Allowed(reader) {
		t.Error("the reader was locked out by someone else's guesses")
	}
	// A client connecting directly cannot pick its own address.
	if got := ClientIP(from("192.0.2.7:1234", "198.51.100.4")); got != "192.0.2.7" {
		t.Errorf("X-Forwarded-For believed from a non-loopback peer: %s", got)
	}
	now = now.Add(l.Window + time.Second)
	if !l.Allowed(attacker) {
		t.Error("still refused after the window")
	}
}

// A sender behind the proxy cannot choose the address it is counted as by
// writing its own X-Forwarded-For: the proxy appends the real one, and that
// is the one taken.
func TestLimiterCannotBeDodged(t *testing.T) {
	l := NewLimiter()
	guess := func(fwd ...string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		for _, f := range fwd {
			r.Header.Add("X-Forwarded-For", f)
		}
		return r
	}
	for i := range l.Max {
		// A fresh made-up address each time, then what Funnel saw, then
		// the gateway's own loopback hop.
		r := guess(fmt.Sprintf("10.0.0.%d, 203.0.113.9", i), "127.0.0.1")
		if !l.Allowed(r) {
			t.Fatalf("refused after %d guesses", i)
		}
		l.Failed(r)
	}
	if l.Allowed(guess("10.9.9.9, 203.0.113.9")) {
		t.Error("a forged first X-Forwarded-For entry reset the count")
	}

	// A right password with the guesser's own account forgives nothing.
	l.Succeeded(guess("203.0.113.9"))
	if l.Allowed(guess("203.0.113.9")) {
		t.Error("signing in with one account cleared the guesses at others")
	}

	// Addresses in one IPv6 /64 are one client.
	for i := range l.Max {
		l.Failed(guess(fmt.Sprintf("2001:db8:1:2::%x", i+1)))
	}
	if l.Allowed(guess("2001:db8:1:2:ffff::1")) {
		t.Error("another address in the same /64 was a new client")
	}
	if !l.Allowed(guess("2001:db8:1:3::1")) {
		t.Error("the next /64 was refused")
	}
}

// An account made through the open registration endpoint is marked, and an
// operator's account of the same name made later is not: the mark goes with
// the account, so a deleted stranger cannot leave its access, or its lack of
// it, to whoever comes next.
func TestSelfRegisteredAccountsAreMarked(t *testing.T) {
	store := newStore(t)
	if err := store.RegisterUser("stranger", "k"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser("owner", "k"); err != nil {
		t.Fatal(err)
	}
	if self, err := store.SelfRegistered(" stranger "); err != nil || !self {
		t.Errorf("registered account: self=%v err=%v, want marked", self, err)
	}
	if self, err := store.SelfRegistered("owner"); err != nil || self {
		t.Errorf("operator's account: self=%v err=%v, want unmarked", self, err)
	}
	if err := store.DeleteUser("stranger"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser("stranger", "k"); err != nil {
		t.Fatal(err)
	}
	if self, _ := store.SelfRegistered("stranger"); self {
		t.Error("an operator's account inherited a deleted registration's mark")
	}

	// The registration endpoint is what marks it.
	srv := httptest.NewServer(NewServer(store, nil).Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/users/create", "application/json",
		strings.NewReader(`{"username":"reader","password":"k"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if self, _ := store.SelfRegistered("reader"); resp.StatusCode != http.StatusCreated || !self {
		t.Errorf("POST /users/create: %d, marked=%v", resp.StatusCode, self)
	}
}

// Admin is a grant: a new account, from any route, is a reader account; an
// admin is made on purpose; and upgrading a store made before roles keeps every
// operator account an admin so the owner is not locked out.
func TestAdminIsAGrant(t *testing.T) {
	store := newStore(t)
	if err := store.CreateUser("x4", "k"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAdmin("me", "k"); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterUser("stranger", "k"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"x4": false, "me": true, "stranger": false} {
		if got, err := store.IsAdmin(name); err != nil || got != want {
			t.Errorf("IsAdmin(%s) = %v, %v; want %v", name, got, err, want)
		}
	}
	if err := store.SetAdmin("x4", true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.IsAdmin("x4"); !ok {
		t.Error("SetAdmin(true) did not grant")
	}
	if err := store.SetAdmin("nobody", true); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("SetAdmin on a missing account: %v", err)
	}
	// A deleted admin's name, reused, starts as a reader.
	if err := store.DeleteUser("me"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser("me", "k"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.IsAdmin("me"); ok {
		t.Error("a recreated account inherited admin")
	}
	users, _ := store.UserList()
	roles := map[string]bool{}
	for _, u := range users {
		roles[u.Username] = u.Admin
	}
	if !roles["x4"] || roles["me"] || roles["stranger"] {
		t.Errorf("UserList roles = %v", roles)
	}
}

func TestUpgradeMakesExistingOperatorAccountsAdmins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser("owner", "k"); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterUser("stranger", "k"); err != nil {
		t.Fatal(err)
	}
	// Stand in for a store from before roles existed.
	if _, err := store.db.Exec(`DROP TABLE admins`); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if ok, _ := store.IsAdmin("owner"); !ok {
		t.Error("an account from before roles lost its access on upgrade")
	}
	if ok, _ := store.IsAdmin("stranger"); ok {
		t.Error("a self-registered account became an admin on upgrade")
	}
}
