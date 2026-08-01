package device

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// uploadHTTP transfers a file with POST /upload, the fallback when the
// WebSocket handshake fails.
//
// This path gives no progress frames, so progress is reported as the bytes are
// streamed into the request rather than as the device acknowledges them. That
// is a weaker signal, which is one reason the WebSocket path is preferred.
func (c *Client) uploadHTTP(ctx context.Context, dest Path, r io.Reader, size int64, opts UploadOptions) error {
	if err := checkWritable(dest); err != nil {
		return err
	}

	// Stream the multipart body rather than buffering the file in memory; a
	// library can contain books far larger than is comfortable to hold.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		part, err := mw.CreateFormFile("file", dest.Base())
		if err != nil {
			pw.CloseWithError(err)
			return
		}

		counter := &countingWriter{
			w:    part,
			dest: dest,
			size: size,
			opts: opts,
		}
		if _, err := io.Copy(counter, r); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := mw.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()

	u := c.baseURL("/upload") + "?path=" + url.QueryEscape(dest.Dir().String())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := strings.TrimSpace(string(body))

	if resp.StatusCode != http.StatusOK {
		if devErr := ParseDeviceError(text); devErr != nil {
			return devErr
		}
		return fmt.Errorf("upload %s: device returned %s", dest, resp.Status)
	}
	// The firmware reports some failures with a 200 status and an error body.
	if strings.HasPrefix(text, "ERROR") {
		return ParseDeviceError(text)
	}

	report(opts, Progress{Path: dest, Sent: size, Total: size})
	return nil
}

// countingWriter reports progress as bytes are written.
type countingWriter struct {
	w    io.Writer
	dest Path
	size int64
	sent int64
	opts UploadOptions
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.sent += int64(n)
	report(c.opts, Progress{Path: c.dest, Sent: c.sent, Total: c.size})
	return n, err
}
