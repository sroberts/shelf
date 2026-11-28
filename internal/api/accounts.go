package api

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sroberts/shelf/internal/kosync"
)

// AccountStore is where reader accounts live; *kosync.Store satisfies it.
//
// A reader account is what the e-reader signs in with, for progress sync and
// for the OPDS catalog alike. It is separate from an Outpost login because the
// reader cannot sign in to Outpost: its paths are public, and shelf checks
// these accounts itself.
type AccountStore interface {
	UserList() ([]kosync.UserInfo, error)
	CreateUser(username, key string) error
	SetPassword(username, key string) error
	DeleteUser(username string) error
}

// Account is a reader account. Password is set only in the response that
// created or reset it, when shelf generated it: it is never stored in the
// clear and cannot be shown again.
type Account struct {
	Name    string `json:"name"`
	Created int64  `json:"created_unix,omitempty"`
	Books   int    `json:"books"`
	// Admin accounts may also use the web UI, API and MCP. Roles are set
	// from the command line only (shelf user role), deliberately: granting
	// that much access should take a step outside any one browser session.
	Admin    bool   `json:"admin"`
	Password string `json:"password,omitempty"`
}

// Accounts lists reader accounts with how many books each has a position in.
func (s *Service) Accounts() ([]Account, error) {
	if s.AccountDB == nil {
		return nil, badRequest("reader accounts are not available")
	}
	users, err := s.AccountDB.UserList()
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(users))
	for _, u := range users {
		out = append(out, Account{Name: u.Username, Created: u.CreatedAt, Books: u.Books, Admin: u.Admin})
	}
	return out, nil
}

// CreateAccount adds a reader account. With no password, shelf generates one
// that is easy to type on the reader and strong enough to face the internet.
func (s *Service) CreateAccount(name, password string) (*Account, error) {
	if s.AccountDB == nil {
		return nil, badRequest("reader accounts are not available")
	}
	name = strings.TrimSpace(name)
	if err := checkAccountName(name); err != nil {
		return nil, err
	}
	generated := password == ""
	if generated {
		password = kosync.GeneratePassword()
	} else if err := checkPassword(password); err != nil {
		return nil, err
	}
	err := s.AccountDB.CreateUser(name, kosync.PasswordKey(password))
	if errors.Is(err, kosync.ErrUserExists) {
		return nil, fmt.Errorf("%w: there is already an account called %q", ErrConflict, name)
	}
	if err != nil {
		return nil, err
	}
	out := &Account{Name: name}
	if generated {
		out.Password = password
	}
	return out, nil
}

// ResetPassword gives an account a new password, generated when none is given.
// The reader signs in again with it; its reading positions are kept.
func (s *Service) ResetPassword(name, password string) (*Account, error) {
	if s.AccountDB == nil {
		return nil, badRequest("reader accounts are not available")
	}
	generated := password == ""
	if generated {
		password = kosync.GeneratePassword()
	} else if err := checkPassword(password); err != nil {
		return nil, err
	}
	err := s.AccountDB.SetPassword(name, kosync.PasswordKey(password))
	if errors.Is(err, kosync.ErrNoSuchUser) {
		return nil, fmt.Errorf("%w: no account called %q", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	out := &Account{Name: name}
	if generated {
		out.Password = password
	}
	return out, nil
}

// DeleteAccount removes an account and every reading position it holds.
func (s *Service) DeleteAccount(name string) error {
	if s.AccountDB == nil {
		return badRequest("reader accounts are not available")
	}
	err := s.AccountDB.DeleteUser(name)
	if errors.Is(err, kosync.ErrNoSuchUser) {
		return fmt.Errorf("%w: no account called %q", ErrNotFound, name)
	}
	return err
}

// checkAccountName refuses names the reader's sign-in cannot carry. HTTP Basic
// joins name and password with a colon, so a colon in the name would split in
// the wrong place; a slash would not survive the /v1/accounts/{name} path.
func checkAccountName(name string) error {
	switch {
	case name == "":
		return badRequest("an account needs a name")
	case len(name) > 64:
		return badRequest("an account name can be at most 64 characters")
	case strings.ContainsAny(name, ":/\\"):
		return badRequest("an account name cannot contain a colon or a slash")
	}
	return nil
}

// checkPassword refuses a chosen password too short to put on the internet.
// The reader's paths may be public under Funnel, and the address limiter only
// slows guessing; it does not make a four-letter password safe.
func checkPassword(p string) error {
	if len(p) < 10 {
		return badRequest("choose a password of at least 10 characters, or leave it empty for shelf to make one")
	}
	return nil
}
