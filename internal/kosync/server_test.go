package kosync

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()

	store, err := OpenStore(filepath.Join(t.TempDir(), "kosync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := NewServer(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

// do issues a request the way the firmware does: x-auth-user and x-auth-key
// headers, plus Basic auth, plus the KOReader Accept header.
func do(t *testing.T, ts *httptest.Server, method, path, user, password string, body any) (*http.Response, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/vnd.koreader.v1+json")
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("x-auth-user", user)
		req.Header.Set("x-auth-key", PasswordKey(password))
		req.Header.Set("Authorization", "Basic "+
			base64.StdEncoding.EncodeToString([]byte(user+":"+password)))
	}

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func TestRegisterAndAuthenticate(t *testing.T) {
	_, ts := testServer(t)

	resp, body := do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "hunter2"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", resp.StatusCode, body)
	}

	resp, body = do(t, ts, "GET", "/users/auth", "scott", "hunter2", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth = %d, want 200: %s", resp.StatusCode, body)
	}
	var out map[string]string
	json.Unmarshal(body, &out)
	if out["authorized"] != "OK" {
		t.Errorf("body = %s, want authorized OK", body)
	}
}

// kosync answers a taken username with 402, which clients recognise.
func TestDuplicateRegistration(t *testing.T) {
	_, ts := testServer(t)

	do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "hunter2"})

	resp, _ := do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "different"})
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("duplicate create = %d, want 402", resp.StatusCode)
	}
}

// A wrong password and an unknown user must be indistinguishable, or the
// endpoint becomes a username oracle.
func TestBadCredentialsAreIndistinguishable(t *testing.T) {
	_, ts := testServer(t)
	do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "hunter2"})

	wrongPass, bodyA := do(t, ts, "GET", "/users/auth", "scott", "wrong", nil)
	noSuchUser, bodyB := do(t, ts, "GET", "/users/auth", "nobody", "hunter2", nil)

	if wrongPass.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", wrongPass.StatusCode)
	}
	if noSuchUser.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown user = %d, want 401", noSuchUser.StatusCode)
	}
	if string(bodyA) != string(bodyB) {
		t.Errorf("responses differ, leaking whether the user exists:\n %s\n %s", bodyA, bodyB)
	}
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	_, ts := testServer(t)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/users/auth"},
		{"PUT", "/syncs/progress"},
		{"GET", "/syncs/progress/abc123"},
	} {
		resp, _ := do(t, ts, tc.method, tc.path, "", "", map[string]string{"document": "abc123"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// The full round trip the device performs: push progress, read it back.
func TestProgressRoundTrip(t *testing.T) {
	_, ts := testServer(t)
	do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "hunter2"})

	const doc = "9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	put := map[string]any{
		"document":   doc,
		"progress":   "/body/DocFragment[3]/body/div/p[17]/text()",
		"percentage": 0.4213,
		"device":     "CrossPoint",
		"device_id":  "abc123",
	}

	resp, body := do(t, ts, "PUT", "/syncs/progress", "scott", "hunter2", put)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put = %d: %s", resp.StatusCode, body)
	}
	var putOut map[string]any
	json.Unmarshal(body, &putOut)
	if putOut["document"] != doc {
		t.Errorf("put echoed document %v", putOut["document"])
	}
	if putOut["timestamp"] == nil {
		t.Error("put did not return a timestamp")
	}

	resp, body = do(t, ts, "GET", "/syncs/progress/"+doc, "scott", "hunter2", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d: %s", resp.StatusCode, body)
	}

	var got Progress
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Document != doc {
		t.Errorf("document = %q", got.Document)
	}
	if got.Percentage != 0.4213 {
		t.Errorf("percentage = %v, want 0.4213", got.Percentage)
	}
	if !strings.Contains(got.Progress, "DocFragment[3]") {
		t.Errorf("progress xpath lost: %q", got.Progress)
	}
	if got.Device != "CrossPoint" {
		t.Errorf("device = %q", got.Device)
	}
	if got.Timestamp == 0 {
		t.Error("timestamp not recorded")
	}
}

// A book that has never been opened is not an error; clients expect an empty
// 200 rather than a 404.
func TestUnknownDocumentReturnsEmptyOK(t *testing.T) {
	_, ts := testServer(t)
	do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "hunter2"})

	resp, body := do(t, ts, "GET", "/syncs/progress/neveropened", "scott", "hunter2", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= %d, want 200 with an empty object: %s", resp.StatusCode, body)
	}
	var out map[string]any
	json.Unmarshal(body, &out)
	if len(out) != 0 {
		t.Errorf("body = %s, want {}", body)
	}
}

func TestProgressIsPerUser(t *testing.T) {
	_, ts := testServer(t)
	for _, u := range []string{"alice", "bob"} {
		do(t, ts, "POST", "/users/create", "", "",
			map[string]string{"username": u, "password": "pw"})
	}

	const doc = "sharedhash"
	do(t, ts, "PUT", "/syncs/progress", "alice", "pw",
		map[string]any{"document": doc, "percentage": 0.9})

	// Bob has the same book but has not read it; he must not see Alice's place.
	_, body := do(t, ts, "GET", "/syncs/progress/"+doc, "bob", "pw", nil)
	var out map[string]any
	json.Unmarshal(body, &out)
	if len(out) != 0 {
		t.Errorf("bob sees alice's progress: %s", body)
	}
}

func TestLatestProgressWins(t *testing.T) {
	_, ts := testServer(t)
	do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "pw"})

	const doc = "d"
	do(t, ts, "PUT", "/syncs/progress", "scott", "pw",
		map[string]any{"document": doc, "percentage": 0.1, "progress": "early"})
	do(t, ts, "PUT", "/syncs/progress", "scott", "pw",
		map[string]any{"document": doc, "percentage": 0.8, "progress": "later"})

	_, body := do(t, ts, "GET", "/syncs/progress/"+doc, "scott", "pw", nil)
	var got Progress
	json.Unmarshal(body, &got)
	if got.Percentage != 0.8 || got.Progress != "later" {
		t.Errorf("got %v / %q, want the most recent write", got.Percentage, got.Progress)
	}
}

// The CrossPoint position extension is stored verbatim when it arrives. It
// normally will not — the firmware only sends it to its own sync server — but
// accepting it costs nothing and means shelf is ready if that ever changes.
func TestCrossPointPositionExtensionIsStored(t *testing.T) {
	_, ts := testServer(t)
	do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "pw"})

	const doc = "withposition"
	put := map[string]any{
		"document":   doc,
		"percentage": 0.33,
		"position": map[string]any{
			"pctQ":  33000,
			"spine": 4,
			"page":  112,
			"pages": 340,
			"xpath": "/body/DocFragment[4]/body/div/p[9]/text()",
		},
	}
	resp, body := do(t, ts, "PUT", "/syncs/progress", "scott", "pw", put)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put = %d: %s", resp.StatusCode, body)
	}

	_, body = do(t, ts, "GET", "/syncs/progress/"+doc, "scott", "pw", nil)
	var got Progress
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Position) == 0 {
		t.Fatalf("position extension was dropped: %s", body)
	}

	var pos map[string]any
	if err := json.Unmarshal(got.Position, &pos); err != nil {
		t.Fatalf("position is not valid JSON: %v", err)
	}
	if pos["spine"] != float64(4) || pos["page"] != float64(112) {
		t.Errorf("position fields lost: %v", pos)
	}
}

// The firmware sends Basic auth alongside the headers; either alone must work.
func TestBasicAuthAloneIsAccepted(t *testing.T) {
	_, ts := testServer(t)
	do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "hunter2"})

	req, _ := http.NewRequest("GET", ts.URL+"/users/auth", nil)
	req.Header.Set("Authorization", "Basic "+
		base64.StdEncoding.EncodeToString([]byte("scott:hunter2")))

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("basic-auth-only request = %d, want 200", resp.StatusCode)
	}
}

func TestRegistrationCanBeDisabled(t *testing.T) {
	srv, ts := testServer(t)
	srv.AllowRegistration = false

	resp, _ := do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "intruder", "password": "pw"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("= %d, want 403 when registration is disabled", resp.StatusCode)
	}
}

func TestMalformedRequests(t *testing.T) {
	_, ts := testServer(t)
	do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "pw"})

	t.Run("not json", func(t *testing.T) {
		req, _ := http.NewRequest("PUT", ts.URL+"/syncs/progress",
			strings.NewReader("{{{not json"))
		req.Header.Set("x-auth-user", "scott")
		req.Header.Set("x-auth-key", PasswordKey("pw"))
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("= %d, want 400", resp.StatusCode)
		}
	})

	t.Run("missing document", func(t *testing.T) {
		resp, _ := do(t, ts, "PUT", "/syncs/progress", "scott", "pw",
			map[string]any{"percentage": 0.5})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("= %d, want 400", resp.StatusCode)
		}
	})

	t.Run("empty username on create", func(t *testing.T) {
		resp, _ := do(t, ts, "POST", "/users/create", "", "",
			map[string]string{"username": "", "password": "pw"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("= %d, want 400", resp.StatusCode)
		}
	})
}

// An oversized body must be refused rather than read into memory.
func TestOversizedBodyIsRejected(t *testing.T) {
	_, ts := testServer(t)
	do(t, ts, "POST", "/users/create", "", "",
		map[string]string{"username": "scott", "password": "pw"})

	huge := `{"document":"d","progress":"` + strings.Repeat("A", maxBodyBytes*2) + `"}`
	req, _ := http.NewRequest("PUT", ts.URL+"/syncs/progress", strings.NewReader(huge))
	req.Header.Set("x-auth-user", "scott")
	req.Header.Set("x-auth-key", PasswordKey("pw"))

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("= %d, want 400 for an oversized body", resp.StatusCode)
	}
}

func TestRootIsHelpful(t *testing.T) {
	_, ts := testServer(t)
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("= %d", resp.StatusCode)
	}
	// Someone checking reachability from a phone should learn what this is.
	if !strings.Contains(string(body), "kosync") {
		t.Errorf("root page does not identify itself: %s", body)
	}
}
