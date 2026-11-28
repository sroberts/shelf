package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxUpload bounds one upload request. Large enough for a scanned PDF, small
// enough that a runaway client cannot fill the storage directory in one go.
var maxUpload int64 = 512 << 20

// transferTimeout replaces the server's write and read deadlines for the two
// routes that move whole files.
const transferTimeout = 30 * time.Minute

func (s *Server) me(w http.ResponseWriter, r *http.Request, user string) {
	who, _ := s.svc.progressFor("", user)
	info := s.Info
	info.Features.Progress = s.svc.Progress != nil
	info.Features.Sync = s.svc.Sync != nil
	info.Features.Upload = s.svc.ImportTemplate != "" && s.svc.StageDir != ""
	info.Features.AI = info.Features.AI || s.svc.Genres != nil
	info.Features.Accounts = s.svc.AccountDB != nil
	writeJSON(w, http.StatusOK, Me{User: user, Info: info, Version: s.svc.Version, ProgressUser: who})
}

func (s *Server) patchBook(w http.ResponseWriter, r *http.Request, user string) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	var p BookPatch
	if !s.decodeBody(w, r, &p, "body must be an object of fields to change") {
		return
	}
	res, err := s.svc.UpdateBook(id, p, user)
	s.respond(w, r, res, err)
}

func (s *Server) getCover(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	img, mtime, err := s.svc.Cover(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(img))
	// Private: the cover is behind a login. Revalidated rather than cached
	// for long, because an edit can change it while the URL stays the same.
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", time.Unix(mtime, 0), bytes.NewReader(img))
}

// uploadBooks takes multipart/form-data with one or more "file" parts. Each
// part is streamed to the staging directory rather than parsed into memory,
// since a single PDF can be hundreds of megabytes.
func (s *Server) uploadBooks(w http.ResponseWriter, r *http.Request, user string) {
	if s.svc.StageDir == "" || s.svc.ImportTemplate == "" {
		writeError(w, http.StatusServiceUnavailable, "uploads are not configured")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(transferTimeout))
	_ = rc.SetWriteDeadline(time.Now().Add(transferTimeout))
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)

	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, `send multipart/form-data with "file" parts`)
		return
	}
	if err := os.MkdirAll(s.svc.StageDir, 0o700); err != nil {
		s.fail(w, r, err)
		return
	}

	var uploads []Upload
	defer func() { removeStaged(uploads) }()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.uploadFailed(w, r, err)
			return
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		name := uploadName(part.FileName())
		if name == "" {
			part.Close()
			s.fail(w, r, badRequest("every file part needs a file name"))
			return
		}
		// Each file gets its own directory so it keeps the name it was sent
		// with: the import reads that name when the file has no metadata.
		dir, err := os.MkdirTemp(s.svc.StageDir, "upload-*")
		if err != nil {
			part.Close()
			s.fail(w, r, err)
			return
		}
		path := filepath.Join(dir, name)
		uploads = append(uploads, Upload{Name: name, Path: path})
		if err := writeFile(path, part); err != nil {
			part.Close()
			s.uploadFailed(w, r, err)
			return
		}
		part.Close()
	}
	if len(uploads) == 0 {
		s.fail(w, r, badRequest(`no "file" parts in the upload`))
		return
	}

	res, err := s.svc.ImportFiles(r.Context(), uploads, user)
	s.respond(w, r, res, err)
}

func (s *Server) uploadFailed(w http.ResponseWriter, r *http.Request, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("an upload can be at most %d MiB; send fewer files at a time", maxUpload>>20))
		return
	}
	s.fail(w, r, badRequest("could not read the upload: %v", err))
}

// uploadName keeps only the base of a client's file name, so a name such as
// "../../config.toml" cannot place a file anywhere but the staging directory.
func uploadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimSpace(name)
	if name == "." || name == "/" || name == ".." || strings.HasPrefix(name, ".") {
		return ""
	}
	return name
}

func writeFile(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *Server) createShelf(w http.ResponseWriter, r *http.Request, _ string) {
	var body struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	}
	if !s.decodeBody(w, r, &body, `body must be {"name": "...", "query": "..."}`) {
		return
	}
	res, err := s.svc.CreateShelf(body.Name, body.Query)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) deleteShelf(w http.ResponseWriter, r *http.Request, _ string) {
	if err := s.svc.DeleteShelf(r.PathValue("name")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clearProgress(w http.ResponseWriter, r *http.Request, user string) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.ClearProgress(id, r.URL.Query().Get("user"), user); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// syncService is the sync half of the API, or a 503 when there is none.
func (s *Server) syncService(w http.ResponseWriter) (*SyncService, bool) {
	if s.svc.Sync == nil {
		writeError(w, http.StatusServiceUnavailable, "syncing is not available")
		return nil, false
	}
	return s.svc.Sync, true
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request, _ string) {
	ss, ok := s.syncService(w)
	if !ok {
		return
	}
	res, err := ss.Devices()
	s.respond(w, r, map[string]any{"devices": res}, err)
}

func (s *Server) addDevice(w http.ResponseWriter, r *http.Request, _ string) {
	ss, ok := s.syncService(w)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
		Host string `json:"host"`
		Root string `json:"root"`
	}
	if !s.decodeBody(w, r, &body, `body must be {"name": "...", "host": "..."}`) {
		return
	}
	res, err := ss.AddDevice(body.Name, body.Host, body.Root)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) startSync(w http.ResponseWriter, r *http.Request, _ string) {
	if _, ok := s.syncService(w); !ok {
		return
	}
	var req SyncRequest
	if !s.decodeBody(w, r, &req, `body must be {"shelf": "", "prune": false, "dry_run": true}`) {
		return
	}
	req.Device = r.PathValue("name")
	res, err := s.svc.StartSync(req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, _ string) {
	ss, ok := s.syncService(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": ss.Jobs()})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request, _ string) {
	ss, ok := s.syncService(w)
	if !ok {
		return
	}
	res, err := ss.Job(r.PathValue("id"))
	s.respond(w, r, res, err)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, _ string) {
	ss, ok := s.syncService(w)
	if !ok {
		return
	}
	res, err := ss.Cancel(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request, _ string) {
	res, err := s.svc.Accounts()
	s.respond(w, r, map[string]any{"accounts": res}, err)
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request, _ string) {
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !s.decodeBody(w, r, &body, `body must be {"name": "..."} (a password is generated when none is given)`) {
		return
	}
	res, err := s.svc.CreateAccount(body.Name, body.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A generated password appears in this response and nowhere else.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request, _ string) {
	var body struct {
		Password string `json:"password"`
	}
	if !s.decodeBody(w, r, &body, `body must be {} to generate a password, or {"password": "..."}`) {
		return
	}
	res, err := s.svc.ResetPassword(r.PathValue("name"), body.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, _ string) {
	if err := s.svc.DeleteAccount(r.PathValue("name")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
