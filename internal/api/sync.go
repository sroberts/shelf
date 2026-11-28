package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/library"
	syncpkg "github.com/sroberts/shelf/internal/sync"
	"github.com/sroberts/shelf/internal/syncrun"
)

// SyncService lists the configured devices and runs syncs to them as jobs.
//
// A sync takes minutes: the reader is a microcontroller on Wi-Fi taking one
// upload at a time. So a client starts a job and polls it, rather than
// holding a request open past every proxy's patience. Jobs run on the server's
// own context, not the request's, and are waited for on shutdown so a SIGTERM
// never closes the index under one.
type SyncService struct {
	// ConfigFile is where AddDevice appends a [[device]] block.
	ConfigFile string
	// Prepare plans a sync; tests replace it. Defaults to syncrun.Prepare.
	Prepare func(ctx context.Context, cfg *config.Config, dev config.Device, books []*library.Book, opts syncrun.Options) (Session, error)

	ctx context.Context
	wg  sync.WaitGroup

	mu      sync.Mutex
	cfg     *config.Config
	jobs    map[string]*job
	order   []string          // job ids, oldest first
	running map[string]string // device nickname -> id of its live job
}

// Session is a planned sync, ready to run once; *syncrun.Session satisfies it.
type Session interface {
	PlanOf() syncpkg.Plan
	NotesOf() []string
	Execute(ctx context.Context, events func(syncpkg.Event)) (*syncpkg.Result, error)
}

// keepJobs bounds the finished jobs kept for clients to read back.
const keepJobs = 20

// maxPlanOps bounds the operations a job reports. A first sync of a large
// library plans thousands of uploads; a client needs the counts and a sample.
const maxPlanOps = 500

// NewSyncService runs jobs under ctx, which should end when the server stops.
func NewSyncService(ctx context.Context, cfg *config.Config, configFile string) *SyncService {
	return &SyncService{
		ConfigFile: configFile,
		ctx:        ctx,
		cfg:        cfg,
		jobs:       map[string]*job{},
		running:    map[string]string{},
	}
}

// Wait blocks until every job has stopped. Cancel the context first.
func (ss *SyncService) Wait() { ss.wg.Wait() }

func (ss *SyncService) config() *config.Config {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.cfg
}

// Device is a configured reader.
type Device struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Host      string `json:"host,omitempty"`
	Mount     string `json:"mount,omitempty"`
	Root      string `json:"root"`
	Default   bool   `json:"default"`
	// LastSync is when shelf last finished a sync to it, if ever.
	LastSync int64 `json:"last_sync_unix,omitempty"`
	// Books is how many books shelf has placed on it.
	Books int `json:"books"`
	// Job is the id of a sync running to it now.
	Job string `json:"job,omitempty"`
}

// Devices lists the configured readers.
func (ss *SyncService) Devices() ([]Device, error) {
	cfg := ss.config()
	def, _ := cfg.DeviceByName("")
	out := make([]Device, 0, len(cfg.Devices))
	for _, d := range cfg.Devices {
		out = append(out, ss.describe(cfg, d, d.Nickname == def.Nickname))
	}
	return out, nil
}

func (ss *SyncService) describe(cfg *config.Config, d config.Device, isDefault bool) Device {
	out := Device{
		Name: d.Nickname, Transport: string(d.Transport), Host: d.Host, Mount: d.Mount,
		Root: d.Root, Default: isDefault,
	}
	if out.Transport == "" {
		out.Transport = string(config.TransportWS)
	}
	// The manifest is shelf's record of the device; reading it touches only
	// local state, so listing devices never waits on a reader that is asleep.
	if m, err := syncpkg.LoadManifest(cfg.Paths.DeviceStateFile(d.Nickname), d.Nickname, d.Root); err == nil {
		out.LastSync, out.Books = m.LastSyncUnix, len(m.Entries)
	}
	ss.mu.Lock()
	out.Job = ss.running[d.Nickname]
	ss.mu.Unlock()
	return out
}

// AddDevice appends a Wi-Fi reader to config.toml. The file is appended to,
// never rewritten, so a hand-written config keeps its comments.
func (ss *SyncService) AddDevice(name, host, root string) (*Device, error) {
	name, host, root = strings.TrimSpace(name), strings.TrimSpace(host), strings.TrimSpace(root)
	if name == "" || host == "" {
		return nil, badRequest("give the reader a name and an address")
	}
	if strings.ContainsAny(host, "/ ") {
		return nil, badRequest("the address is a name or IP address, such as 192.168.1.40, not a web address")
	}
	if root != "" && !strings.HasPrefix(root, "/") {
		root = "/" + root
	}
	if ss.ConfigFile == "" {
		return nil, errors.New("no config file to add the device to")
	}

	ss.mu.Lock()
	cfg := ss.cfg
	if _, err := cfg.DeviceByName(name); err == nil {
		ss.mu.Unlock()
		return nil, fmt.Errorf("%w: a device named %q already exists", ErrConflict, name)
	}
	updated, err := config.AppendDevice(ss.ConfigFile, cfg.Paths,
		config.Device{Nickname: name, Host: host, Root: root})
	if err != nil {
		ss.mu.Unlock()
		// AppendDevice validates before replacing anything; what it refuses
		// is something the caller sent.
		return nil, badRequest("%v", err)
	}
	// Keep what this process was started with that the file does not hold:
	// flags such as --library override the file.
	updated.LibraryRoot = cfg.LibraryRoot
	ss.cfg = &updated
	ss.mu.Unlock()

	d, err := updated.DeviceByName(name)
	if err != nil {
		return nil, err
	}
	def, _ := updated.DeviceByName("")
	out := ss.describe(&updated, d, def.Nickname == name)
	return &out, nil
}

// SyncRequest starts a job.
type SyncRequest struct {
	Device string `json:"device"`
	Shelf  string `json:"shelf"`
	Prune  bool   `json:"prune"`
	DryRun bool   `json:"dry_run"`
}

// Job is a sync's state, as clients read it.
type Job struct {
	ID       string       `json:"id"`
	Kind     string       `json:"kind"`
	Device   string       `json:"device"`
	Shelf    string       `json:"shelf"`
	DryRun   bool         `json:"dry_run"`
	Prune    bool         `json:"prune"`
	State    string       `json:"state"`
	Started  int64        `json:"started_unix"`
	Finished int64        `json:"finished_unix,omitempty"`
	Plan     *PlanSummary `json:"plan,omitempty"`
	Progress *JobProgress `json:"progress,omitempty"`
	Result   *JobResult   `json:"result,omitempty"`
	Notes    []string     `json:"notes"`
	Error    string       `json:"error,omitempty"`
}

// Job states.
const (
	JobPlanning  = "planning"
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// PlanSummary is what a sync will do, or did.
type PlanSummary struct {
	Uploads    int      `json:"uploads"`
	Deletes    int      `json:"deletes"`
	Moves      int      `json:"moves"`
	Mkdirs     int      `json:"mkdirs"`
	Unchanged  int      `json:"unchanged"`
	TotalBytes int64    `json:"total_bytes"`
	Orphans    int      `json:"orphans"`
	Warnings   []string `json:"warnings"`
	Ops        []PlanOp `json:"ops"`
	// Truncated is true when Ops holds only the first maxPlanOps.
	Truncated bool `json:"truncated,omitempty"`
}

// PlanOp is one planned operation.
type PlanOp struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	From   string `json:"from,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// JobProgress is where a running sync is.
type JobProgress struct {
	Index     int    `json:"index"`
	Total     int    `json:"total"`
	Path      string `json:"path"`
	Sent      int64  `json:"sent"`
	Size      int64  `json:"size"`
	BytesSent int64  `json:"bytes_sent"`
}

// JobResult is how a sync went.
type JobResult struct {
	Completed int      `json:"completed"`
	Failed    int      `json:"failed"`
	Skipped   int      `json:"skipped"`
	BytesSent int64    `json:"bytes_sent"`
	Errors    []string `json:"errors"`
}

type job struct {
	state  Job // guarded by SyncService.mu
	cancel context.CancelFunc
}

// BusyError is a sync refused because the device already has one running.
type BusyError struct{ Job string }

func (e *BusyError) Error() string {
	return fmt.Sprintf("a sync to this device is already running (job %s)", e.Job)
}

func (e *BusyError) Unwrap() error { return ErrConflict }

// Start begins a sync job. books resolves what to send once the job runs, so
// a shelf edited while a preview is read is planned as it then stands.
func (ss *SyncService) Start(req SyncRequest, books func(shelf string) ([]*library.Book, error)) (*Job, error) {
	cfg := ss.config()
	dev, err := cfg.DeviceByName(req.Device)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotFound, err)
	}

	ss.mu.Lock()
	if id, busy := ss.running[dev.Nickname]; busy {
		ss.mu.Unlock()
		return nil, &BusyError{Job: id}
	}
	ctx, cancel := context.WithCancel(ss.ctx)
	j := &job{cancel: cancel, state: Job{
		ID: newJobID(), Kind: "sync", Device: dev.Nickname, Shelf: req.Shelf,
		DryRun: req.DryRun, Prune: req.Prune, State: JobPlanning,
		Started: time.Now().Unix(), Notes: []string{},
	}}
	ss.jobs[j.state.ID] = j
	ss.order = append(ss.order, j.state.ID)
	ss.running[dev.Nickname] = j.state.ID
	ss.trimLocked()
	snapshot := j.state
	ss.mu.Unlock()

	ss.wg.Add(1)
	go func() {
		defer ss.wg.Done()
		defer cancel()
		ss.run(ctx, j, cfg, dev, req, books)
	}()
	return &snapshot, nil
}

func (ss *SyncService) run(ctx context.Context, j *job, cfg *config.Config, dev config.Device,
	req SyncRequest, books func(string) ([]*library.Book, error)) {

	finish := func(state string, err error) {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		if ctx.Err() != nil && state == JobFailed {
			state = JobCancelled
		}
		j.state.State = state
		if err != nil && state != JobCancelled {
			j.state.Error = err.Error()
		}
		j.state.Finished = time.Now().Unix()
		j.state.Progress = nil
		delete(ss.running, dev.Nickname)
	}

	list, err := books(req.Shelf)
	if err != nil {
		finish(JobFailed, err)
		return
	}
	if len(list) == 0 {
		name := req.Shelf
		if name == "" {
			name = "the library"
		}
		finish(JobFailed, fmt.Errorf("nothing to sync: %s is empty", name))
		return
	}

	prepare := ss.Prepare
	if prepare == nil {
		prepare = func(ctx context.Context, cfg *config.Config, dev config.Device, books []*library.Book, opts syncrun.Options) (Session, error) {
			s, err := syncrun.Prepare(ctx, cfg, dev, books, opts)
			if err != nil {
				return nil, err
			}
			return sessionAdapter{s}, nil
		}
	}
	session, err := prepare(ctx, cfg, dev, list, syncrun.Options{Prune: req.Prune})
	if err != nil {
		finish(JobFailed, err)
		return
	}

	plan := session.PlanOf()
	ss.mu.Lock()
	j.state.Plan = summarize(plan)
	j.state.Notes = append(j.state.Notes, session.NotesOf()...)
	if !req.DryRun {
		j.state.State = JobRunning
	}
	ss.mu.Unlock()
	if req.DryRun {
		finish(JobDone, nil)
		return
	}

	var bytesSent int64
	res, err := session.Execute(ctx, func(e syncpkg.Event) {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		if e.Kind == syncpkg.EventDone && e.Op.Kind == syncpkg.OpUpload {
			bytesSent += e.Op.Size
		}
		j.state.Progress = &JobProgress{
			Index: e.Index, Total: e.Total, Path: e.Op.DevicePath.String(),
			Sent: e.Sent, Size: e.Size, BytesSent: bytesSent,
		}
	})
	if res != nil {
		r := &JobResult{Completed: res.Completed, Failed: res.Failed, Skipped: res.Skipped,
			BytesSent: res.BytesSent, Errors: []string{}}
		for _, e := range res.Errors {
			r.Errors = append(r.Errors, e.Error())
		}
		ss.mu.Lock()
		j.state.Result = r
		j.state.Notes = session.NotesOf()
		ss.mu.Unlock()
	}
	switch {
	case err != nil:
		finish(JobFailed, err)
	case res != nil && res.Failed > 0:
		finish(JobFailed, fmt.Errorf("%d operation(s) failed", res.Failed))
	default:
		finish(JobDone, nil)
	}
}

// trimLocked forgets the oldest finished jobs past keepJobs.
func (ss *SyncService) trimLocked() {
	for len(ss.order) > keepJobs {
		dropped := false
		for i, id := range ss.order {
			if st := ss.jobs[id].state.State; st != JobPlanning && st != JobRunning {
				delete(ss.jobs, id)
				ss.order = append(ss.order[:i], ss.order[i+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			return // every kept job is live; one per device bounds this
		}
	}
}

// Jobs lists recent jobs, newest first.
func (ss *SyncService) Jobs() []Job {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	out := make([]Job, 0, len(ss.order))
	for i := len(ss.order) - 1; i >= 0; i-- {
		out = append(out, ss.jobs[ss.order[i]].state)
	}
	return out
}

// Job returns one job.
func (ss *SyncService) Job(id string) (*Job, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	j, ok := ss.jobs[id]
	if !ok {
		return nil, fmt.Errorf("%w: no job %q", ErrNotFound, id)
	}
	out := j.state
	return &out, nil
}

// Cancel stops a job. A cancelled sync stops between operations or mid
// upload; the reader deletes a partial file, and the manifest records every
// book that finished, so the next sync resumes rather than starting over.
func (ss *SyncService) Cancel(id string) (*Job, error) {
	ss.mu.Lock()
	j, ok := ss.jobs[id]
	if !ok {
		ss.mu.Unlock()
		return nil, fmt.Errorf("%w: no job %q", ErrNotFound, id)
	}
	j.cancel()
	out := j.state
	ss.mu.Unlock()
	return &out, nil
}

func summarize(p syncpkg.Plan) *PlanSummary {
	mkdirs, uploads, deletes, moves := p.Counts()
	out := &PlanSummary{
		Uploads: uploads, Deletes: deletes, Moves: moves, Mkdirs: mkdirs,
		Unchanged: p.Unchanged, TotalBytes: p.TotalBytes, Orphans: len(p.Orphans),
		Warnings: append([]string{}, p.Warnings...), Ops: []PlanOp{},
	}
	for _, op := range p.Ops {
		if len(out.Ops) == maxPlanOps {
			out.Truncated = true
			break
		}
		o := PlanOp{Kind: string(op.Kind), Path: op.DevicePath.String(), Size: op.Size, Reason: op.Reason}
		if op.From != "" {
			o.From = op.From.String()
		}
		out.Ops = append(out.Ops, o)
	}
	return out
}

func newJobID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// sessionAdapter fits *syncrun.Session to Session.
type sessionAdapter struct{ s *syncrun.Session }

func (a sessionAdapter) PlanOf() syncpkg.Plan { return a.s.Plan }
func (a sessionAdapter) NotesOf() []string {
	return append(append([]string{}, a.s.Warnings...), a.s.Notes...)
}
func (a sessionAdapter) Execute(ctx context.Context, events func(syncpkg.Event)) (*syncpkg.Result, error) {
	return a.s.Execute(ctx, events)
}

// SyncBooks resolves a shelf name, or "" for the whole library, to its books.
func (s *Service) SyncBooks(shelf string) ([]*library.Book, error) {
	if shelf == "" {
		return s.DB.All()
	}
	books, err := s.DB.ShelfBooks(shelf)
	if errors.Is(err, library.ErrShelfNotFound) {
		return nil, fmt.Errorf("no shelf named %q", shelf)
	}
	return books, err
}

// StartSync checks the shelf exists before starting a job, so a typo is a
// 404 now rather than a failed job later.
func (s *Service) StartSync(req SyncRequest) (*Job, error) {
	if s.Sync == nil {
		return nil, badRequest("syncing is not available")
	}
	if req.Shelf != "" {
		if _, err := s.DB.GetShelf(req.Shelf); errors.Is(err, library.ErrShelfNotFound) {
			return nil, fmt.Errorf("%w: no shelf named %q", ErrNotFound, req.Shelf)
		} else if err != nil {
			return nil, err
		}
	}
	return s.Sync.Start(req, s.SyncBooks)
}
