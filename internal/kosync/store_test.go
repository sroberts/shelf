package kosync

import (
	"errors"
	"path/filepath"
	"testing"
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
