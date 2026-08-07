package kosync

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// The kosync HTTP API.
//
//	POST /users/create              {username, password} -> 201
//	GET  /users/auth                                     -> 200 {authorized}
//	PUT  /syncs/progress            {document, ...}      -> 200 {document, timestamp}
//	GET  /syncs/progress/<document>                      -> 200 {document, ...}
//
// Authentication is the x-auth-user and x-auth-key headers, where the key is
// MD5 of the password. CrossPoint's firmware additionally sends HTTP Basic with
// the plaintext password, so both are accepted.

// maxBodyBytes caps request bodies. Progress payloads are a few hundred bytes;
// anything approaching this is a mistake or an attack.
const maxBodyBytes = 64 << 10

// Server implements the kosync API over a Store.
type Server struct {
	store *Store
	log   *slog.Logger

	// AllowRegistration controls whether POST /users/create works.
	//
	// The device has no other way to make an account, so this defaults on. It
	// is worth being able to turn off: this server is meant to sit on a home
	// network, and an open registration endpoint on a listener bound to all
	// interfaces is an invitation.
	AllowRegistration bool
}

// NewServer builds a server over a store.
func NewServer(store *Store, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{store: store, log: log, AllowRegistration: true}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users/create", s.handleCreateUser)
	mux.HandleFunc("GET /users/auth", s.handleAuth)
	mux.HandleFunc("PUT /syncs/progress", s.handlePutProgress)
	mux.HandleFunc("GET /syncs/progress/{document}", s.handleGetProgress)

	// A bare GET is useful for confirming, from a phone on the same network,
	// that the thing the device is being pointed at is actually reachable.
	mux.HandleFunc("GET /", s.handleRoot)

	return s.withLogging(mux)
}

// credentials carries what a request presented.
type credentials struct {
	username string
	key      string
}

// extractCredentials reads auth from the headers.
//
// The x-auth-key header already carries MD5(password). Basic auth carries the
// password itself, which CrossPoint sends alongside, so it is hashed here to
// arrive at the same value.
func extractCredentials(r *http.Request) credentials {
	user := NormalizeUsername(r.Header.Get("x-auth-user"))
	key := strings.TrimSpace(r.Header.Get("x-auth-key"))

	if user != "" && key != "" {
		return credentials{username: user, key: key}
	}

	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Basic ") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
		if err == nil {
			if name, password, ok := strings.Cut(string(raw), ":"); ok {
				if user == "" {
					user = NormalizeUsername(name)
				}
				if key == "" {
					key = PasswordKey(password)
				}
			}
		}
	}
	return credentials{username: user, key: key}
}

// authenticate verifies a request, writing the error response itself.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	c := extractCredentials(r)
	if c.username == "" || c.key == "" {
		writeError(w, http.StatusUnauthorized, 2001, "Unauthorized")
		return "", false
	}

	if err := s.store.Authenticate(c.username, c.key); err != nil {
		// The response is deliberately identical for an unknown user and a
		// wrong key: telling them apart would turn this into a username
		// oracle. The log is a different audience — it is the operator's own
		// machine, and without the attempted username a failed login is
		// undiagnosable.
		s.log.Info("authentication failed",
			"username", c.username,
			"reason", authFailureReason(err),
			"remote", r.RemoteAddr,
			"hint", s.authHint(err))

		writeError(w, http.StatusUnauthorized, 2001, "Unauthorized")
		return "", false
	}
	return c.username, true
}

// authFailureReason names why authentication failed, for the log only.
func authFailureReason(err error) string {
	switch {
	case errors.Is(err, ErrNoSuchUser):
		return "no such user"
	case errors.Is(err, ErrBadPassword):
		return "wrong password"
	default:
		return err.Error()
	}
}

// authHint suggests what to do about it.
//
// The commonest cause by far is a reader logging in before an account exists,
// which looks like a credentials problem from the device and is nothing of the
// sort. Saying so here saves an evening.
func (s *Server) authHint(err error) string {
	if !errors.Is(err, ErrNoSuchUser) {
		return "check the password"
	}
	if users, e := s.store.Users(); e == nil && len(users) == 0 {
		return "no accounts exist yet — use the reader's Register action, not Login"
	}
	return "username not registered"
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.AllowRegistration {
		writeError(w, http.StatusForbidden, 2003, "Registration is disabled")
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}

	username := NormalizeUsername(body.Username)
	if username == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, 2003, "Invalid request")
		return
	}

	// The registration endpoint receives a plaintext password; the sync
	// endpoints receive its MD5. Both reduce to the same stored verifier.
	err := s.store.CreateUser(username, PasswordKey(body.Password))
	switch {
	case errors.Is(err, ErrUserExists):
		// 402 is what kosync uses for "username taken". Odd, but clients
		// recognise it.
		writeError(w, http.StatusPaymentRequired, 2002, "Username is already registered")
		return
	case err != nil:
		s.log.Error("create user failed", "err", err)
		writeError(w, http.StatusInternalServerError, 2000, "Unknown server error")
		return
	}

	s.log.Info("registered user", "username", username)
	writeJSON(w, http.StatusCreated, map[string]string{"username": username})
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorized": "OK"})
}

func (s *Server) handlePutProgress(w http.ResponseWriter, r *http.Request) {
	username, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	var body struct {
		Document   string          `json:"document"`
		Progress   string          `json:"progress"`
		Percentage float64         `json:"percentage"`
		Device     string          `json:"device"`
		DeviceID   string          `json:"device_id"`
		Position   json.RawMessage `json:"position"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	if body.Document == "" {
		writeError(w, http.StatusBadRequest, 2003, "Invalid request")
		return
	}

	ts, err := s.store.PutProgress(username, Progress{
		Document:   body.Document,
		Progress:   body.Progress,
		Percentage: body.Percentage,
		Device:     body.Device,
		DeviceID:   body.DeviceID,
		Position:   body.Position,
	})
	if err != nil {
		s.log.Error("save progress failed", "err", err)
		writeError(w, http.StatusInternalServerError, 2000, "Unknown server error")
		return
	}

	s.log.Info("progress",
		"user", username, "document", body.Document,
		"percent", body.Percentage, "device", body.Device,
		"position", len(body.Position) > 0)

	writeJSON(w, http.StatusOK, map[string]any{
		"document":  body.Document,
		"timestamp": ts,
	})
}

func (s *Server) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	username, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	document := r.PathValue("document")
	if document == "" {
		writeError(w, http.StatusBadRequest, 2003, "Invalid request")
		return
	}

	p, err := s.store.GetProgress(username, document)
	if errors.Is(err, ErrNoProgress) {
		// KOReader clients treat an empty 200 as "nothing recorded yet", which
		// is not an error condition — it is every book you have not opened.
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if err != nil {
		s.log.Error("read progress failed", "err", err)
		writeError(w, http.StatusInternalServerError, 2000, "Unknown server error")
		return
	}

	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "shelf kosync server\n\n"+
		"Point your reader's KOReader sync setting at this address.\n"+
		"Endpoints: /users/create, /users/auth, /syncs/progress\n")
}

// withLogging records each request at debug level.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Debug("request",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "remote", r.RemoteAddr)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// decodeJSON reads a JSON body, writing an error response on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()

	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, 2003, "Invalid request")
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError emits the shape kosync clients expect: a numeric code alongside a
// message, not a bare HTTP status.
func writeError(w http.ResponseWriter, status, code int, message string) {
	writeJSON(w, status, map[string]any{
		"code":    code,
		"message": message,
	})
}
