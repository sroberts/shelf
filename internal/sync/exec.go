package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sroberts/shelf/internal/device"
)

// Transport is the subset of the device client the executor needs. Keeping it
// an interface lets the SD-card transport drop in later without touching the
// executor, and lets tests run the full engine without a network.
type Transport interface {
	Mkdir(ctx context.Context, p device.Path) error
	Upload(ctx context.Context, dest device.Path, r io.Reader, size int64, opts device.UploadOptions) error
	Delete(ctx context.Context, p device.Path) error
	Move(ctx context.Context, from, to device.Path) error
	Status(ctx context.Context) (*device.Status, error)
}

// EventKind classifies a progress event.
type EventKind string

const (
	EventStart    EventKind = "start"
	EventProgress EventKind = "progress"
	EventDone     EventKind = "done"
	EventFailed   EventKind = "failed"
	EventSkipped  EventKind = "skipped"
	EventRetry    EventKind = "retry"
)

// Event reports execution progress. The CLI prints these; the TUI will feed
// them to tea.Program.Send.
type Event struct {
	Kind    EventKind
	Op      Op
	Index   int // 1-based position in the plan
	Total   int // number of operations
	Sent    int64
	Size    int64
	Attempt int
	Err     error
}

// ExecOptions tunes execution.
type ExecOptions struct {
	// InterOpDelay gives the ESP32 time to flush its SD writes between
	// operations. The device is slow to commit and a tight loop provokes
	// write failures.
	InterOpDelay time.Duration

	// MaxRetries bounds attempts per operation. There is no resume, so every
	// retry restarts the file from byte zero.
	MaxRetries int

	// ChunkSize is passed through to the uploader.
	ChunkSize int

	// Events, when set, receives progress.
	Events func(Event)

	// DryRun reports what would happen without touching the device.
	DryRun bool
}

// Result summarizes an execution.
type Result struct {
	Completed  int
	Failed     int
	Skipped    int
	BytesSent  int64
	Errors     []error
	Aborted    bool
	AbortedFor error
}

// Execute runs a plan against a device, serially.
//
// Serial by design, not by omission: the firmware accepts one upload at a time,
// and the device is a microcontroller whose SD writes need breathing room.
func Execute(ctx context.Context, t Transport, plan Plan, m *Manifest, opts ExecOptions) (*Result, error) {
	if opts.MaxRetries < 1 {
		opts.MaxRetries = 3
	}

	res := &Result{}
	emit := func(e Event) {
		if opts.Events != nil {
			opts.Events(e)
		}
	}

	// consecutiveFailures aborts a run that is clearly not going to recover.
	consecutiveFailures := 0

	for i, op := range plan.Ops {
		event := Event{Op: op, Index: i + 1, Total: len(plan.Ops), Size: op.Size}

		// An interrupt finishes the current file, then stops cleanly. Checking
		// here rather than mid-upload is what makes that true.
		if err := ctx.Err(); err != nil {
			res.Aborted, res.AbortedFor = true, err
			return res, err
		}

		if opts.DryRun {
			emit(Event{Kind: EventSkipped, Op: op, Index: i + 1, Total: len(plan.Ops)})
			res.Skipped++
			continue
		}

		emit(Event{Kind: EventStart, Op: op, Index: i + 1, Total: len(plan.Ops), Size: op.Size})

		err := runOp(ctx, t, op, opts, event, emit)
		if err == nil {
			consecutiveFailures = 0
			res.Completed++
			if op.Kind == OpUpload {
				res.BytesSent += op.Size
			}
			recordSuccess(m, op)
			emit(Event{Kind: EventDone, Op: op, Index: i + 1, Total: len(plan.Ops), Size: op.Size})

			if opts.InterOpDelay > 0 && i < len(plan.Ops)-1 {
				if !sleepCtx(ctx, opts.InterOpDelay) {
					res.Aborted, res.AbortedFor = true, ctx.Err()
					return res, ctx.Err()
				}
			}
			continue
		}

		res.Failed++
		res.Errors = append(res.Errors, fmt.Errorf("%s: %w", op, err))
		emit(Event{Kind: EventFailed, Op: op, Index: i + 1, Total: len(plan.Ops), Err: err})

		// A full card or a device that left transfer mode will not fix itself
		// by moving to the next book.
		if device.IsFatal(err) {
			res.Aborted, res.AbortedFor = true, err
			return res, err
		}

		consecutiveFailures++
		if consecutiveFailures >= 3 {
			abortErr := fmt.Errorf("aborting after %d consecutive failures; last error: %w",
				consecutiveFailures, err)
			res.Aborted, res.AbortedFor = true, abortErr
			return res, abortErr
		}
	}

	return res, nil
}

// runOp performs one operation with retries.
func runOp(ctx context.Context, t Transport, op Op, opts ExecOptions, base Event, emit func(Event)) error {
	var lastErr error

	for attempt := 1; attempt <= opts.MaxRetries; attempt++ {
		if attempt > 1 {
			emit(Event{
				Kind: EventRetry, Op: op, Index: base.Index, Total: base.Total,
				Attempt: attempt, Err: lastErr,
			})
			// Back off before retrying; a device that is busy needs time, not
			// a tighter loop.
			if !sleepCtx(ctx, backoff(attempt)) {
				return ctx.Err()
			}
		}

		err := performOp(ctx, t, op, opts, base, emit)
		if err == nil {
			return nil
		}
		lastErr = err

		if !device.IsRetryable(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("after %d attempts: %w", opts.MaxRetries, lastErr)
}

// performOp dispatches a single attempt.
func performOp(ctx context.Context, t Transport, op Op, opts ExecOptions, base Event, emit func(Event)) error {
	switch op.Kind {
	case OpMkdir:
		err := t.Mkdir(ctx, op.DevicePath)
		if err != nil && isAlreadyExists(err) {
			return nil // a directory that already exists is success
		}
		return err

	case OpDelete:
		return t.Delete(ctx, op.DevicePath)

	case OpMove:
		return t.Move(ctx, op.From, op.DevicePath)

	case OpUpload:
		f, err := os.Open(op.LocalPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", op.LocalPath, err)
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			return err
		}
		// The file may have changed since planning; the device validates
		// against the size in the START frame, so send the current size.
		size := info.Size()

		return t.Upload(ctx, op.DevicePath, f, size, device.UploadOptions{
			ChunkSize: opts.ChunkSize,
			Progress: func(p device.Progress) {
				emit(Event{
					Kind: EventProgress, Op: op, Index: base.Index, Total: base.Total,
					Sent: p.Sent, Size: p.Total,
				})
			},
		})

	default:
		return fmt.Errorf("unknown operation %q", op.Kind)
	}
}

// recordSuccess updates the manifest after an operation completes.
func recordSuccess(m *Manifest, op Op) {
	if m == nil {
		return
	}

	switch op.Kind {
	case OpUpload:
		m.Put(Entry{
			SHA256:       op.SHA256,
			DevicePath:   op.DevicePath.String(),
			DeviceSize:   op.Size,
			LocalPath:    op.LocalPath,
			UploadedUnix: time.Now().Unix(),
		})

	case OpDelete:
		m.RemoveDevice(op.DevicePath)

	case OpMove:
		// The move is the only operation that changes a pinned path, and it
		// only happens under an explicit --repath.
		m.Put(Entry{
			SHA256:       op.SHA256,
			DevicePath:   op.DevicePath.String(),
			DeviceSize:   op.Size,
			LocalPath:    op.LocalPath,
			UploadedUnix: time.Now().Unix(),
		})
	}
}

// ApplyDrops removes manifest entries the planner decided to forget.
func ApplyDrops(m *Manifest, drops []string) {
	for _, localPath := range drops {
		m.RemoveLocal(localPath)
	}
}

// WriteDeviceState mirrors shelf's identity and manifest onto the device so a
// second machine can reconstruct the same state.
func WriteDeviceState(ctx context.Context, t Transport, m *Manifest) error {
	if err := t.Mkdir(ctx, device.NewPath(ShelfDir)); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("create %s: %w", ShelfDir, err)
	}

	marker := DeviceMarker{
		Version:    ManifestVersion,
		DeviceUUID: m.DeviceUUID,
		Nickname:   m.Nickname,
		Created:    time.Now().Unix(),
	}
	markerJSON, err := jsonBytes(marker)
	if err != nil {
		return err
	}
	if err := uploadBytes(ctx, t, device.NewPath(MarkerPath), markerJSON); err != nil {
		return fmt.Errorf("write %s: %w", MarkerPath, err)
	}

	manifestJSON, err := m.Bytes()
	if err != nil {
		return err
	}
	if err := uploadBytes(ctx, t, device.NewPath(ManifestPath), manifestJSON); err != nil {
		return fmt.Errorf("write %s: %w", ManifestPath, err)
	}
	return nil
}

func uploadBytes(ctx context.Context, t Transport, dest device.Path, data []byte) error {
	return t.Upload(ctx, dest, bytes.NewReader(data), int64(len(data)), device.UploadOptions{})
}

// isAlreadyExists reports whether an error means the target already exists,
// which mkdir treats as success. The firmware reports this as a plain message
// rather than a distinct status, so it is matched on text.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// backoff returns the delay before a retry attempt.
func backoff(attempt int) time.Duration {
	d := time.Duration(attempt-1) * 500 * time.Millisecond
	if d > 5*time.Second {
		return 5 * time.Second
	}
	return d
}

// sleepCtx sleeps unless the context ends first, reporting whether it completed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// jsonBytes encodes a value as indented JSON with a trailing newline.
func jsonBytes(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
