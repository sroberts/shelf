package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sroberts/shelf/internal/kosync"
)

func TestReaderAccounts(t *testing.T) {
	f := newRealFixture(t)
	store, err := kosync.OpenStore(filepath.Join(t.TempDir(), "progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f.svc.AccountDB = store

	resp, body := f.do(t, "POST", "/v1/accounts", "application/json", `{"name":"x4"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("a response carrying a password must not be cached")
	}
	var acct Account
	json.Unmarshal(body, &acct)
	if acct.Name != "x4" || len(acct.Password) != 19 {
		t.Fatalf("account = %+v", acct)
	}
	// The generated password is the one the reader signs in with.
	if err := store.CheckPassword("x4", acct.Password); err != nil {
		t.Errorf("generated password does not sign in: %v", err)
	}

	// Listing never shows a password.
	var list struct{ Accounts []Account }
	f.getJSON(t, "/v1/accounts", &list)
	if len(list.Accounts) != 1 || list.Accounts[0].Password != "" {
		t.Errorf("list = %+v", list.Accounts)
	}

	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"taken":          {`{"name":"x4"}`, 409},
		"colon":          {`{"name":"a:b"}`, 400},
		"empty":          {`{"name":" "}`, 400},
		"short password": {`{"name":"kobo","password":"hunter2"}`, 400},
	} {
		if resp, b := f.do(t, "POST", "/v1/accounts", "application/json", tc.body); resp.StatusCode != tc.code {
			t.Errorf("%s: %d %s, want %d", name, resp.StatusCode, b, tc.code)
		}
	}

	// A chosen password is accepted and not echoed back.
	resp, body = f.do(t, "POST", "/v1/accounts", "application/json", `{"name":"kobo","password":"correct horse battery"}`)
	if resp.StatusCode != 201 || strings.Contains(string(body), "correct horse") {
		t.Errorf("chosen password: %d %s", resp.StatusCode, body)
	}

	resp, body = f.do(t, "POST", "/v1/accounts/x4/password", "application/json", `{}`)
	var reset Account
	json.Unmarshal(body, &reset)
	if resp.StatusCode != 200 || reset.Password == "" || reset.Password == acct.Password {
		t.Fatalf("reset: %d %s", resp.StatusCode, body)
	}
	if store.CheckPassword("x4", acct.Password) == nil || store.CheckPassword("x4", reset.Password) != nil {
		t.Error("after a reset, only the new password should sign in")
	}
	if resp, _ := f.do(t, "POST", "/v1/accounts/nobody/password", "application/json", `{}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("reset of a missing account: %d", resp.StatusCode)
	}

	if resp, _ := f.do(t, "DELETE", "/v1/accounts/x4", "", ""); resp.StatusCode != 204 {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if resp, _ := f.do(t, "DELETE", "/v1/accounts/x4", "", ""); resp.StatusCode != 404 {
		t.Errorf("second delete: %d", resp.StatusCode)
	}

	var me Me
	f.getJSON(t, "/v1/me", &me)
	if !me.Features.Accounts {
		t.Error("me does not report accounts")
	}
}
