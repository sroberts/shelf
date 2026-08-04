package device

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// WebSocket upload, the primary transfer path.
//
//	client -> START:<filename>:<size>:<path>
//	server -> READY
//	client -> [binary chunks]
//	server -> PROGRESS:<received>:<total>    every 64 KB and at completion
//	server -> DONE | ERROR:<message>
//
// There is no resume. The firmware deletes a partial file on disconnect or
// error, so a retry restarts from byte zero.

// Chunk size bounds, matching the device's RAM budget. The firmware writes
// through a 4 KB buffer and runs with roughly 380 KB of usable heap.
const (
	MinChunkSize     = 4 * 1024
	MaxChunkSize     = 64 * 1024
	DefaultChunkSize = 16 * 1024
)

// wsHandshakeTimeout bounds the initial connection and START/READY exchange.
const wsHandshakeTimeout = 10 * time.Second

// Progress reports upload advancement.
type Progress struct {
	Path  Path
	Sent  int64
	Total int64
}

// Percent returns completion as a percentage, or 0 for an unknown total.
func (p Progress) Percent() float64 {
	if p.Total <= 0 {
		return 0
	}
	return float64(p.Sent) / float64(p.Total) * 100
}

// UploadOptions tunes a single transfer.
type UploadOptions struct {
	// ChunkSize is clamped to [MinChunkSize, MaxChunkSize].
	ChunkSize int
	// Progress, when set, is called as the device acknowledges bytes.
	Progress func(Progress)
	// ForceHTTP skips the WebSocket path entirely.
	ForceHTTP bool
}

func (o UploadOptions) chunkSize() int {
	switch {
	case o.ChunkSize < MinChunkSize:
		return DefaultChunkSize
	case o.ChunkSize > MaxChunkSize:
		return MaxChunkSize
	default:
		return o.ChunkSize
	}
}

// Upload transfers a file to the device.
//
// Uploads are serialized here rather than in the caller. The firmware accepts
// exactly one at a time and rejects a second with "Upload already in progress",
// so holding the lock in the client means no caller anywhere can violate that
// invariant, including a future concurrent sync planner.
func (c *Client) Upload(ctx context.Context, dest Path, r io.Reader, size int64, opts UploadOptions) error {
	if err := checkWritable(dest); err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("%w: negative size", ErrInvalidStart)
	}

	c.uploadMu.Lock()
	defer c.uploadMu.Unlock()

	if opts.ForceHTTP {
		return c.uploadHTTP(ctx, dest, r, size, opts)
	}

	err := c.uploadWS(ctx, dest, r, size, opts)
	if err == nil {
		return nil
	}
	// Only fall back when the WebSocket path was never established. Once the
	// device has accepted a START frame, a failure is a real failure and
	// retrying over HTTP would upload the file twice.
	if !errors.Is(err, errWSUnavailable) {
		return err
	}
	if !seekToStart(r) {
		return fmt.Errorf("websocket upload unavailable and the source cannot be re-read: %w", err)
	}
	return c.uploadHTTP(ctx, dest, r, size, opts)
}

// errWSUnavailable marks a failure that happened before the device committed to
// the transfer, making an HTTP fallback safe.
var errWSUnavailable = errors.New("websocket unavailable")

// seekToStart rewinds a reader so a fallback can resend from byte zero.
func seekToStart(r io.Reader) bool {
	s, ok := r.(io.Seeker)
	if !ok {
		return false
	}
	_, err := s.Seek(0, io.SeekStart)
	return err == nil
}

// uploadWS runs the WebSocket upload state machine.
func (c *Client) uploadWS(ctx context.Context, dest Path, r io.Reader, size int64, opts UploadOptions) error {
	dialCtx, cancelDial := context.WithTimeout(ctx, wsHandshakeTimeout)
	defer cancelDial()

	conn, _, err := websocket.Dial(dialCtx, c.wsURL(), &websocket.DialOptions{
		HTTPClient: c.http,
	})
	if err != nil {
		return fmt.Errorf("%w: dial %s: %v", errWSUnavailable, c.wsURL(), err)
	}
	// A partial upload leaves the device deleting the file; closing with a
	// non-normal status makes the intent explicit.
	defer conn.CloseNow()

	// The device is RAM constrained; keep the read limit small. Its messages
	// are short status strings.
	conn.SetReadLimit(4096)

	// START:<filename>:<size>:<path>
	//
	// The firmware's HTTP upload endpoint takes `path` as the destination
	// *directory* plus a separate multipart filename, so the WebSocket frame is
	// read the same way here. Confirm against hardware when capturing fixtures.
	start := fmt.Sprintf("START:%s:%d:%s", dest.Base(), size, dest.Dir().String())
	if err := conn.Write(ctx, websocket.MessageText, []byte(start)); err != nil {
		return fmt.Errorf("%w: send START: %v", errWSUnavailable, err)
	}

	// Read messages on a goroutine so PROGRESS frames arriving mid-transfer do
	// not deadlock against our writes.
	msgs, readErrs := readLoop(ctx, conn)

	// Wait for READY before sending any bytes.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-readErrs:
		return fmt.Errorf("%w: waiting for READY: %v", errWSUnavailable, err)
	case msg := <-msgs:
		switch {
		case strings.HasPrefix(msg, "READY"):
			// proceed
		case strings.HasPrefix(msg, "ERROR"):
			// The device answered, so the transport works; this is a real
			// device-level refusal and must not fall back to HTTP.
			return ParseDeviceError(msg)
		default:
			return fmt.Errorf("%w: expected READY, got %q", errWSUnavailable, msg)
		}
	case <-time.After(wsHandshakeTimeout):
		return fmt.Errorf("%w: timed out waiting for READY", errWSUnavailable)
	}

	finished, err := c.sendChunks(ctx, conn, dest, r, size, opts, msgs, readErrs)
	if err != nil {
		return err
	}
	if finished {
		// The device already said DONE while the body was still streaming.
		return nil
	}
	return waitForDone(ctx, dest, size, opts, msgs, readErrs)
}

// sendChunks streams the body. It reports whether the device already returned
// a final verdict, so the caller knows not to wait for one.
func (c *Client) sendChunks(
	ctx context.Context, conn *websocket.Conn, dest Path,
	r io.Reader, size int64, opts UploadOptions,
	msgs <-chan string, readErrs <-chan error,
) (done bool, err error) {
	buf := make([]byte, opts.chunkSize())
	var sent int64

	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}

		// Surface a device error mid-transfer rather than sending the whole
		// file into a device that has already given up.
		select {
		case msg := <-msgs:
			if strings.HasPrefix(msg, "ERROR") {
				return false, ParseDeviceError(msg)
			}
			if strings.HasPrefix(msg, "DONE") {
				// The device finished before this loop noticed. That happens
				// when it acknowledges the last chunk quickly, and it is
				// success, not a short write.
				report(opts, Progress{Path: dest, Sent: size, Total: size})
				return true, nil
			}
			if p, ok := parseProgress(msg); ok {
				report(opts, Progress{Path: dest, Sent: p.received, Total: p.total})
			}
		case err := <-readErrs:
			// A closed connection is only a failure if the device did not
			// report success first. It sends DONE and then closes, so the
			// close and the verdict arrive together; treating the close alone
			// as failure would retry an upload that already completed, and on
			// a slow link that means re-sending the whole book.
			if devErr := drainForVerdict(msgs); devErr != nil {
				if errors.Is(devErr, errDeviceDone) {
					report(opts, Progress{Path: dest, Sent: size, Total: size})
					return true, nil
				}
				return false, devErr
			}
			return false, fmt.Errorf("connection lost after %d of %d bytes: %w", sent, size, err)
		default:
		}

		n, readErr := r.Read(buf)
		if n > 0 {
			if err := conn.Write(ctx, websocket.MessageBinary, buf[:n]); err != nil {
				// The device reports a failure and then drops the connection,
				// so a write error is usually the symptom rather than the
				// cause. Give the real message a moment to arrive.
				if devErr := drainForVerdict(msgs); devErr != nil {
					if errors.Is(devErr, errDeviceDone) {
						report(opts, Progress{Path: dest, Sent: size, Total: size})
						return true, nil
					}
					return false, devErr
				}
				return false, fmt.Errorf("send chunk at offset %d: %w", sent, err)
			}
			sent += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return false, fmt.Errorf("read source at offset %d: %w", sent, readErr)
		}
	}

	if sent != size {
		// The device validates against the size in the START frame and will
		// reject a mismatch, so catch it here with a clearer message.
		return false, fmt.Errorf("%w: declared %d bytes but sent %d", ErrInvalidStart, size, sent)
	}
	return false, nil
}

// waitForDone blocks until the device reports completion or failure.
func waitForDone(
	ctx context.Context, dest Path, size int64, opts UploadOptions,
	msgs <-chan string, readErrs <-chan error,
) error {
	for {
		// Already-delivered messages take priority over a connection error.
		// The device sends DONE or ERROR and then closes, so both become
		// ready at once and an unbiased select would report the close and
		// discard the verdict that explains it.
		select {
		case msg, ok := <-msgs:
			if ok {
				if done, err := classify(msg, dest, size, opts); done {
					return err
				}
				continue
			}
			// The message channel closed; fall through to the error below.
		default:
		}

		select {
		case <-ctx.Done():
			return ctx.Err()

		case msg, ok := <-msgs:
			if !ok {
				// Reader finished. Anything buffered has been consumed above.
				return connectionLost(readErrs)
			}
			if done, err := classify(msg, dest, size, opts); done {
				return err
			}

		case err := <-readErrs:
			// Drain anything the reader delivered before failing.
			if devErr := drainForVerdict(msgs); devErr != nil {
				if errors.Is(devErr, errDeviceDone) {
					report(opts, Progress{Path: dest, Sent: size, Total: size})
					return nil
				}
				return devErr
			}
			return fmt.Errorf("connection lost before completion: %w", err)
		}
	}
}

// classify interprets one device message, reporting whether it ends the
// transfer and with what result.
func classify(msg string, dest Path, size int64, opts UploadOptions) (done bool, err error) {
	switch {
	case strings.HasPrefix(msg, "DONE"):
		report(opts, Progress{Path: dest, Sent: size, Total: size})
		return true, nil
	case strings.HasPrefix(msg, "ERROR"):
		return true, ParseDeviceError(msg)
	default:
		if p, ok := parseProgress(msg); ok {
			report(opts, Progress{Path: dest, Sent: p.received, Total: p.total})
		}
		return false, nil
	}
}

// connectionLost builds an error for a closed connection with no verdict.
func connectionLost(readErrs <-chan error) error {
	select {
	case err := <-readErrs:
		return fmt.Errorf("connection lost before completion: %w", err)
	default:
		return errors.New("device closed the connection without reporting DONE")
	}
}

// errDeviceDone reports that the device finished successfully. It travels as an
// error only so drainForVerdict can return one value; callers translate it.
var errDeviceDone = errors.New("device reported DONE")

// drainForVerdict consumes buffered messages looking for the device's final
// word, returning errDeviceDone for success and a typed error for failure.
//
// The device writes its last frame and closes immediately, so by the time a
// write or read failure surfaces, the verdict is usually already sitting in the
// channel. Reading it is the difference between reporting what the device
// actually said and reporting "broken pipe" — and, for DONE, between accepting
// a finished upload and re-sending the whole book.
func drainForVerdict(msgs <-chan string) error {
	deadline := time.After(250 * time.Millisecond)
	for {
		select {
		case msg, ok := <-msgs:
			if !ok {
				return nil
			}
			if strings.HasPrefix(msg, "ERROR") {
				return ParseDeviceError(msg)
			}
			if strings.HasPrefix(msg, "DONE") {
				return errDeviceDone
			}
		case <-deadline:
			return nil
		}
	}
}

func report(opts UploadOptions, p Progress) {
	if opts.Progress != nil {
		opts.Progress(p)
	}
}

// readLoop forwards device messages onto a channel.
func readLoop(ctx context.Context, conn *websocket.Conn) (<-chan string, <-chan error) {
	msgs := make(chan string, 16)
	errs := make(chan error, 1)

	go func() {
		defer close(msgs)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
			select {
			case msgs <- strings.TrimSpace(string(data)):
			case <-ctx.Done():
				return
			}
		}
	}()
	return msgs, errs
}

// progressFrame is a decoded PROGRESS message.
type progressFrame struct {
	received int64
	total    int64
}

// parseProgress decodes "PROGRESS:<received>:<total>".
func parseProgress(msg string) (progressFrame, bool) {
	if !strings.HasPrefix(msg, "PROGRESS:") {
		return progressFrame{}, false
	}
	parts := strings.Split(strings.TrimPrefix(msg, "PROGRESS:"), ":")
	if len(parts) != 2 {
		return progressFrame{}, false
	}

	received, err1 := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	total, err2 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err1 != nil || err2 != nil {
		return progressFrame{}, false
	}
	return progressFrame{received: received, total: total}, true
}
