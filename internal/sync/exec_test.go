package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sroberts/shelf/internal/device"
)

// fakeTransport records operations and can inject failures, so the executor is
// tested without a network or hardware.
type fakeTransport struct {
	mu sync.Mutex

	calls    []string
	uploaded map[string][]byte
	dirs     map[string]bool

	// Injected failures, keyed by device path. Each entry is consumed once per
	// attempt so retry behaviour can be exercised.
	failUpload map[string][]error
	failMkdir  map[string]error
	failDelete map[string]error

	// concurrency tracking
	active, maxActive int
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		uploaded:   map[string][]byte{},
		dirs:       map[string]bool{},
		failUpload: map[string][]error{},
		failMkdir:  map[string]error{},
		failDelete: map[string]error{},
	}
}

func (f *fakeTransport) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeTransport) Status(context.Context) (*device.Status, error) {
	return &device.Status{Version: device.TestedVersion, Device: device.ModelX4}, nil
}

func (f *fakeTransport) Mkdir(_ context.Context, p device.Path) error {
	f.record("mkdir " + p.String())
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failMkdir[p.String()]; err != nil {
		return err
	}
	f.dirs[p.String()] = true
	return nil
}

func (f *fakeTransport) Delete(_ context.Context, p device.Path) error {
	f.record("delete " + p.String())
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failDelete[p.String()]; err != nil {
		return err
	}
	delete(f.uploaded, p.String())
	return nil
}

func (f *fakeTransport) Move(_ context.Context, from, to device.Path) error {
	f.record(fmt.Sprintf("move %s %s", from, to))
	f.mu.Lock()
	defer f.mu.Unlock()
	body := f.uploaded[from.String()]
	delete(f.uploaded, from.String())
	f.uploaded[to.String()] = body
	return nil
}

func (f *fakeTransport) Upload(_ context.Context, dest device.Path, r io.Reader, size int64, opts device.UploadOptions) error {
	f.record("upload " + dest.String())

	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	queued := f.failUpload[dest.String()]
	if len(queued) > 0 {
		err := queued[0]
		f.failUpload[dest.String()] = queued[1:]
		f.active--
		f.mu.Unlock()
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.active++
	}
	f.mu.Unlock()

	body, err := io.ReadAll(r)

	f.mu.Lock()
	f.active--
	if err == nil {
		f.uploaded[dest.String()] = body
	}
	f.mu.Unlock()

	if err != nil {
		return err
	}
	if opts.Progress != nil {
		opts.Progress(device.Progress{Path: dest, Sent: size, Total: size})
	}
	return nil
}

func (f *fakeTransport) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// writeTempBook creates a local file to upload.
func writeTempBook(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, size)
	for i := range body {
		body[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecuteHappyPath(t *testing.T) {
	dir := t.TempDir()
	local := writeTempBook(t, dir, "moby.epub", 1000)

	plan := Plan{Ops: []Op{
		{Kind: OpMkdir, DevicePath: device.NewPath("/Books")},
		{Kind: OpMkdir, DevicePath: device.NewPath("/Books/Melville")},
		{Kind: OpUpload, DevicePath: device.NewPath("/Books/Melville/moby.epub"),
			LocalPath: local, Size: 1000, SHA256: "abc"},
	}}

	tr := newFakeTransport()
	m := NewManifest("x4", "/Books")

	res, err := Execute(context.Background(), tr, plan, m, ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Completed != 3 || res.Failed != 0 {
		t.Errorf("result = %+v", res)
	}
	if res.BytesSent != 1000 {
		t.Errorf("BytesSent = %d, want 1000", res.BytesSent)
	}

	if body := tr.uploaded["/Books/Melville/moby.epub"]; len(body) != 1000 {
		t.Errorf("uploaded %d bytes, want 1000", len(body))
	}

	// The manifest must record the placement, which is what makes the next
	// sync a no-op.
	entries := m.ByLocalPath()
	e, ok := entries[local]
	if !ok {
		t.Fatal("the manifest has no entry for the uploaded book")
	}
	if e.DevicePath != "/Books/Melville/moby.epub" || e.SHA256 != "abc" || e.DeviceSize != 1000 {
		t.Errorf("manifest entry = %+v", e)
	}
	if e.UploadedUnix == 0 {
		t.Error("UploadedUnix was not set")
	}
}

func TestExecuteDryRunTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	local := writeTempBook(t, dir, "a.epub", 100)

	plan := Plan{Ops: []Op{
		{Kind: OpMkdir, DevicePath: device.NewPath("/Books")},
		{Kind: OpUpload, DevicePath: device.NewPath("/Books/a.epub"), LocalPath: local, Size: 100},
	}}

	tr := newFakeTransport()
	m := NewManifest("x4", "/Books")

	res, err := Execute(context.Background(), tr, plan, m, ExecOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 2 || res.Completed != 0 {
		t.Errorf("result = %+v", res)
	}
	if len(tr.callLog()) != 0 {
		t.Errorf("a dry run reached the device: %v", tr.callLog())
	}
	if len(m.Entries) != 0 {
		t.Errorf("a dry run modified the manifest: %v", m.Entries)
	}
}

// A full SD card must abort the entire run, not just the current file.
func TestExecuteAbortsOnDiskFull(t *testing.T) {
	dir := t.TempDir()
	a := writeTempBook(t, dir, "a.epub", 100)
	b := writeTempBook(t, dir, "b.epub", 100)

	plan := Plan{Ops: []Op{
		{Kind: OpUpload, DevicePath: device.NewPath("/Books/a.epub"), LocalPath: a, Size: 100},
		{Kind: OpUpload, DevicePath: device.NewPath("/Books/b.epub"), LocalPath: b, Size: 100},
	}}

	tr := newFakeTransport()
	tr.failUpload["/Books/a.epub"] = []error{device.ErrDiskFull}

	res, err := Execute(context.Background(), tr, plan, NewManifest("x4", "/Books"), ExecOptions{})
	if !errors.Is(err, device.ErrDiskFull) {
		t.Fatalf("err = %v, want ErrDiskFull", err)
	}
	if !res.Aborted {
		t.Error("the run should be marked aborted")
	}

	// The second book must never have been attempted.
	for _, call := range tr.callLog() {
		if strings.Contains(call, "b.epub") {
			t.Errorf("the run continued past a full disk: %v", tr.callLog())
		}
	}
}

// A retryable failure must be retried, and succeed when the device recovers.
func TestExecuteRetriesTransientFailures(t *testing.T) {
	dir := t.TempDir()
	local := writeTempBook(t, dir, "a.epub", 100)

	plan := Plan{Ops: []Op{
		{Kind: OpUpload, DevicePath: device.NewPath("/Books/a.epub"), LocalPath: local, Size: 100},
	}}

	tr := newFakeTransport()
	// Fail twice with a retryable error, then succeed.
	tr.failUpload["/Books/a.epub"] = []error{device.ErrUploadInProgress, device.ErrUploadInProgress}

	var retries int
	res, err := Execute(context.Background(), tr, plan, NewManifest("x4", "/Books"), ExecOptions{
		MaxRetries: 3,
		Events: func(e Event) {
			if e.Kind == EventRetry {
				retries++
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Completed != 1 {
		t.Errorf("Completed = %d, want 1", res.Completed)
	}
	if retries != 2 {
		t.Errorf("retries = %d, want 2", retries)
	}
}

// A non-retryable failure must not be retried.
func TestExecuteDoesNotRetryPermanentFailures(t *testing.T) {
	dir := t.TempDir()
	local := writeTempBook(t, dir, "a.epub", 100)

	plan := Plan{Ops: []Op{
		{Kind: OpUpload, DevicePath: device.NewPath("/Books/a.epub"), LocalPath: local, Size: 100},
	}}

	tr := newFakeTransport()
	tr.failUpload["/Books/a.epub"] = []error{
		device.ErrProtectedPath, device.ErrProtectedPath, device.ErrProtectedPath,
	}

	var attempts int
	_, err := Execute(context.Background(), tr, plan, NewManifest("x4", "/Books"), ExecOptions{
		MaxRetries: 3,
		Events: func(e Event) {
			if e.Kind == EventStart {
				attempts++
			}
		},
	})
	if err != nil {
		// A single failed op is reported in Result, not as a returned error.
		t.Logf("err = %v", err)
	}
	if attempts != 1 {
		t.Errorf("start events = %d, want 1", attempts)
	}
	// Exactly one upload call: no retries.
	var uploads int
	for _, c := range tr.callLog() {
		if strings.HasPrefix(c, "upload") {
			uploads++
		}
	}
	if uploads != 1 {
		t.Errorf("upload attempts = %d, want 1 (permanent errors must not be retried)", uploads)
	}
}

// Three consecutive failures abort the run.
func TestExecuteAbortsAfterConsecutiveFailures(t *testing.T) {
	dir := t.TempDir()

	var ops []Op
	tr := newFakeTransport()
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("b%d.epub", i)
		local := writeTempBook(t, dir, name, 10)
		dest := "/Books/" + name
		ops = append(ops, Op{Kind: OpUpload, DevicePath: device.NewPath(dest), LocalPath: local, Size: 10})
		// A permanent, non-fatal error so each op fails without aborting alone.
		tr.failUpload[dest] = []error{device.ErrNotFound}
	}

	res, err := Execute(context.Background(), tr, Plan{Ops: ops}, NewManifest("x4", "/Books"),
		ExecOptions{MaxRetries: 1})
	if err == nil {
		t.Fatal("expected the run to abort")
	}
	if !strings.Contains(err.Error(), "consecutive failures") {
		t.Errorf("err = %v", err)
	}
	if !res.Aborted {
		t.Error("the run should be marked aborted")
	}
	if res.Failed != 3 {
		t.Errorf("Failed = %d, want 3 before aborting", res.Failed)
	}
}

// mkdir on an existing directory is success, not failure.
func TestExecuteTreatsExistingDirectoryAsSuccess(t *testing.T) {
	plan := Plan{Ops: []Op{{Kind: OpMkdir, DevicePath: device.NewPath("/Books")}}}

	tr := newFakeTransport()
	tr.failMkdir["/Books"] = errors.New("ERROR:Directory already exists")

	res, err := Execute(context.Background(), tr, plan, NewManifest("x4", "/Books"), ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Completed != 1 || res.Failed != 0 {
		t.Errorf("result = %+v; an existing directory should count as success", res)
	}
}

// An interrupt finishes the current operation and then stops cleanly.
func TestExecuteStopsOnCancellation(t *testing.T) {
	dir := t.TempDir()

	var ops []Op
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("b%d.epub", i)
		ops = append(ops, Op{
			Kind: OpUpload, DevicePath: device.NewPath("/Books/" + name),
			LocalPath: writeTempBook(t, dir, name, 10), Size: 10,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	tr := newFakeTransport()

	var completed int
	_, err := Execute(ctx, tr, Plan{Ops: ops}, NewManifest("x4", "/Books"), ExecOptions{
		Events: func(e Event) {
			if e.Kind == EventDone {
				completed++
				if completed == 2 {
					cancel()
				}
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if completed > 3 {
		t.Errorf("%d operations completed after cancellation; it should stop promptly", completed)
	}
}

// The executor is serial by design: the firmware accepts one upload at a time.
func TestExecuteIsSerial(t *testing.T) {
	dir := t.TempDir()

	var ops []Op
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("b%d.epub", i)
		ops = append(ops, Op{
			Kind: OpUpload, DevicePath: device.NewPath("/Books/" + name),
			LocalPath: writeTempBook(t, dir, name, 1000), Size: 1000,
		})
	}

	tr := newFakeTransport()
	if _, err := Execute(context.Background(), tr, Plan{Ops: ops},
		NewManifest("x4", "/Books"), ExecOptions{}); err != nil {
		t.Fatal(err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.maxActive > 1 {
		t.Errorf("%d uploads overlapped; the executor must be serial", tr.maxActive)
	}
}

// The inter-op delay gives the ESP32 time to flush SD writes.
func TestExecuteRespectsInterOpDelay(t *testing.T) {
	dir := t.TempDir()
	ops := []Op{
		{Kind: OpUpload, DevicePath: device.NewPath("/Books/a.epub"),
			LocalPath: writeTempBook(t, dir, "a.epub", 10), Size: 10},
		{Kind: OpUpload, DevicePath: device.NewPath("/Books/b.epub"),
			LocalPath: writeTempBook(t, dir, "b.epub", 10), Size: 10},
		{Kind: OpUpload, DevicePath: device.NewPath("/Books/c.epub"),
			LocalPath: writeTempBook(t, dir, "c.epub", 10), Size: 10},
	}

	const delay = 40 * time.Millisecond
	start := time.Now()
	if _, err := Execute(context.Background(), newFakeTransport(), Plan{Ops: ops},
		NewManifest("x4", "/Books"), ExecOptions{InterOpDelay: delay}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	// Two gaps between three operations; no delay after the last.
	if elapsed < 2*delay {
		t.Errorf("elapsed %v, want at least %v", elapsed, 2*delay)
	}
	if elapsed > 3*delay+time.Second {
		t.Errorf("elapsed %v is far longer than expected", elapsed)
	}
}

// A delete must remove the entry from the manifest, so the book is not
// resurrected on the next sync.
func TestExecuteDeleteUpdatesManifest(t *testing.T) {
	m := NewManifest("x4", "/Books")
	m.Put(Entry{LocalPath: "/l/a.epub", DevicePath: "/Books/a.epub", SHA256: "a", DeviceSize: 10})

	plan := Plan{Ops: []Op{{Kind: OpDelete, DevicePath: device.NewPath("/Books/a.epub")}}}

	if _, err := Execute(context.Background(), newFakeTransport(), plan, m, ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 0 {
		t.Errorf("manifest still holds the deleted book: %v", m.Entries)
	}
}

// A move under --repath must update the pinned path.
func TestExecuteMoveRepins(t *testing.T) {
	m := NewManifest("x4", "/Books")
	m.Put(Entry{LocalPath: "/l/a.epub", DevicePath: "/Books/Old.epub", SHA256: "a", DeviceSize: 10})

	plan := Plan{Ops: []Op{{
		Kind: OpMove, From: device.NewPath("/Books/Old.epub"),
		DevicePath: device.NewPath("/Books/New.epub"),
		LocalPath:  "/l/a.epub", Size: 10, SHA256: "a",
	}}}

	if _, err := Execute(context.Background(), newFakeTransport(), plan, m, ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	e := m.ByLocalPath()["/l/a.epub"]
	if e == nil || e.DevicePath != "/Books/New.epub" {
		t.Errorf("entry = %+v, want the new pinned path", e)
	}
}

func TestExecuteReportsMissingLocalFile(t *testing.T) {
	plan := Plan{Ops: []Op{{
		Kind: OpUpload, DevicePath: device.NewPath("/Books/a.epub"),
		LocalPath: "/nonexistent/a.epub", Size: 10,
	}}}

	res, _ := Execute(context.Background(), newFakeTransport(), plan,
		NewManifest("x4", "/Books"), ExecOptions{MaxRetries: 1})
	if res.Failed != 1 {
		t.Errorf("Failed = %d, want 1", res.Failed)
	}
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Error(), "a.epub") {
		t.Errorf("errors = %v", res.Errors)
	}
}

func TestWriteDeviceState(t *testing.T) {
	tr := newFakeTransport()
	m := NewManifest("x4", "/Books")
	m.Put(Entry{LocalPath: "/l/a.epub", DevicePath: "/Books/a.epub", SHA256: "a", DeviceSize: 10})

	if err := WriteDeviceState(context.Background(), tr, m); err != nil {
		t.Fatal(err)
	}

	marker, ok := tr.uploaded[MarkerPath]
	if !ok {
		t.Fatalf("%s was not written", MarkerPath)
	}
	if !strings.Contains(string(marker), m.DeviceUUID) {
		t.Errorf("the marker does not carry the device UUID: %s", marker)
	}

	manifestJSON, ok := tr.uploaded[ManifestPath]
	if !ok {
		t.Fatalf("%s was not written", ManifestPath)
	}
	if !strings.Contains(string(manifestJSON), "/Books/a.epub") {
		t.Errorf("the mirrored manifest is missing entries: %s", manifestJSON)
	}
}

func TestApplyDrops(t *testing.T) {
	m := NewManifest("x4", "/Books")
	m.Put(Entry{LocalPath: "/l/a.epub", DevicePath: "/Books/a.epub"})
	m.Put(Entry{LocalPath: "/l/b.epub", DevicePath: "/Books/b.epub"})

	ApplyDrops(m, []string{"/l/a.epub"})

	if len(m.Entries) != 1 || m.Entries[0].LocalPath != "/l/b.epub" {
		t.Errorf("entries = %+v", m.Entries)
	}
}

func TestBackoffIsBounded(t *testing.T) {
	if backoff(1) != 0 {
		t.Errorf("the first attempt should not wait, got %v", backoff(1))
	}
	if backoff(2) <= 0 {
		t.Error("later attempts should back off")
	}
	if got := backoff(100); got > 5*time.Second {
		t.Errorf("backoff(100) = %v, want it capped", got)
	}
}
