// Package device talks to a CrossPoint e-reader over its HTTP and WebSocket
// interfaces.
//
// The firmware runs on an ESP32-C3 with roughly 380 KB of usable RAM and
// accepts exactly one upload at a time. Every design choice here follows from
// that: transfers are serialized behind a mutex owned by the client rather than
// the caller, chunks are small, and retries are conservative.
package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Default ports, from the firmware's documented network surface.
const (
	HTTPPort      = 80
	WebSocketPort = 81
	DiscoveryPort = 8134
)

// Timeouts. The device is a microcontroller on Wi-Fi, so these are generous
// compared with a normal HTTP client.
const (
	defaultStatusTimeout = 5 * time.Second
	defaultDialTimeout   = 5 * time.Second
	defaultTimeout       = 60 * time.Second
)

// Status is the response from GET /api/status.
type Status struct {
	Version  string `json:"version"`
	IP       string `json:"ip"`
	Mode     string `json:"mode"` // STA or AP
	RSSI     int    `json:"rssi"` // dBm; 0 in AP mode
	FreeHeap int64  `json:"freeHeap"`
	Uptime   int64  `json:"uptime"`
	Device   string `json:"device"` // X3 or X4
}

// FileEntry is one item from GET /api/files.
//
// Note what is absent: no modification time and no checksum. Change detection
// must never depend on either.
type FileEntry struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	IsDirectory bool   `json:"isDirectory"`
	IsEpub      bool   `json:"isEpub"`
}

// Client talks to one device.
type Client struct {
	host string
	http *http.Client

	// uploadMu serializes uploads. The firmware rejects a concurrent transfer
	// with "Upload already in progress", and this lives in the client rather
	// than the sync planner so that no future caller can bypass it.
	uploadMu sync.Mutex

	// wsURLOverride redirects the WebSocket endpoint. The real device serves
	// HTTP on 80 and WebSocket on 81, but a test server has a single port.
	wsURLOverride string

	mu     sync.Mutex
	status *Status // cached from the last poll
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the underlying HTTP client, for tests.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithTimeout sets the overall request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

// New creates a client for a device reachable at host, which may be a bare
// hostname, an IP, or either with a port.
func New(host string, opts ...Option) *Client {
	c := &Client{
		host: normalizeHost(host),
		http: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: defaultDialTimeout}).DialContext,
				MaxIdleConnsPerHost: 2,
				DisableCompression:  true, // the device does not compress
			},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// normalizeHost strips any scheme and trailing slash, leaving host[:port].
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "ws://")
	return strings.TrimSuffix(host, "/")
}

// Host returns the configured host.
func (c *Client) Host() string { return c.host }

// baseURL builds an HTTP URL for a device endpoint.
func (c *Client) baseURL(endpoint string) string {
	host := c.host
	if !strings.Contains(host, ":") {
		host = fmt.Sprintf("%s:%d", host, HTTPPort)
	}
	return "http://" + host + endpoint
}

// wsURL builds the WebSocket URL used for uploads. The device serves it on a
// different port from HTTP, so any explicit HTTP port is discarded.
func (c *Client) wsURL() string {
	if c.wsURLOverride != "" {
		return c.wsURLOverride
	}
	host := c.host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return fmt.Sprintf("ws://%s:%d/", host, WebSocketPort)
}

// Status polls the device.
//
// A connection failure here is reported as ErrNotInTransfer rather than a raw
// network error, because that is nearly always what it means: the device is
// asleep or not in File Transfer mode.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL("/api/status"), nil)
	if err != nil {
		return nil, err
	}

	client := *c.http
	client.Timeout = defaultStatusTimeout

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrNotInTransfer, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status endpoint returned %s", ErrNotInTransfer, resp.Status)
	}

	var s Status
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}

	c.mu.Lock()
	c.status = &s
	c.mu.Unlock()
	return &s, nil
}

// CachedStatus returns the most recent status poll, if any.
func (c *Client) CachedStatus() *Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Ping reports whether the device is reachable and in transfer mode.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Status(ctx)
	return err
}

// List returns the contents of a device directory.
//
// A missing directory is not an error on real hardware: firmware 1.4.1 answers
// 200 with an empty array rather than 404. The 404 branch below is kept for
// other firmware revisions, but callers must not rely on ErrNotFound to detect
// a missing directory -- use Stat against the parent instead.
func (c *Client) List(ctx context.Context, p Path) ([]FileEntry, error) {
	u := c.baseURL("/api/files") + "?path=" + url.QueryEscape(p.String())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	if err := checkHTTPStatus(resp); err != nil {
		return nil, fmt.Errorf("list %s: %w", p, err)
	}

	var entries []FileEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode listing of %s: %w", p, err)
	}
	return entries, nil
}

// ListRecursive walks a device directory tree, returning every file with its
// full path. Directories that cannot be read are skipped rather than aborting
// the walk.
func (c *Client) ListRecursive(ctx context.Context, root Path) (map[Path]FileEntry, error) {
	out := map[Path]FileEntry{}
	queue := []Path{root}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return out, err
		}

		dir := queue[0]
		queue = queue[1:]

		entries, err := c.List(ctx, dir)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // a missing directory is an empty one
			}
			return out, err
		}

		for _, e := range entries {
			full := dir.Join(e.Name)
			// Never descend into the firmware's cache directories.
			if IsProtected(full) {
				continue
			}
			if e.IsDirectory {
				queue = append(queue, full)
				continue
			}
			out[full] = e
		}
	}
	return out, nil
}

// Stat returns the entry for a single device path.
func (c *Client) Stat(ctx context.Context, p Path) (FileEntry, error) {
	entries, err := c.List(ctx, p.Dir())
	if err != nil {
		return FileEntry{}, err
	}
	name := p.Base()
	for _, e := range entries {
		if e.Name == name {
			return e, nil
		}
	}
	return FileEntry{}, fmt.Errorf("%w: %s", ErrNotFound, p)
}

// Download fetches a file's contents. The caller closes the reader.
func (c *Client) Download(ctx context.Context, p Path) (io.ReadCloser, error) {
	u := c.baseURL("/download") + "?path=" + url.QueryEscape(p.String())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	if err := checkHTTPStatus(resp); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: %w", p, err)
	}
	return resp.Body, nil
}

// Mkdir creates a directory on the device.
func (c *Client) Mkdir(ctx context.Context, p Path) error {
	if err := checkWritable(p); err != nil {
		return err
	}
	return c.postForm(ctx, "/mkdir", url.Values{
		"name": {p.Base()},
		"path": {p.Dir().String()},
	})
}

// MkdirAll creates a directory and any missing parents, shallowest first.
func (c *Client) MkdirAll(ctx context.Context, p Path) error {
	if err := checkWritable(p); err != nil {
		return err
	}
	for _, ancestor := range append(p.Ancestors(), p) {
		if ancestor == "/" {
			continue
		}
		// The firmware reports an existing directory as an error; that is not
		// a failure for MkdirAll, so only a hard error stops the loop.
		if err := c.Mkdir(ctx, ancestor); err != nil {
			if exists, checkErr := c.dirExists(ctx, ancestor); checkErr == nil && exists {
				continue
			}
			return err
		}
	}
	return nil
}

// dirExists reports whether a device path exists and is a directory.
func (c *Client) dirExists(ctx context.Context, p Path) (bool, error) {
	e, err := c.Stat(ctx, p)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return e.IsDirectory, nil
}

// Rename renames a file. The firmware supports this for files only, not folders.
func (c *Client) Rename(ctx context.Context, p Path, newName string) error {
	if err := checkWritable(p); err != nil {
		return err
	}
	if err := checkWritable(p.Dir().Join(newName)); err != nil {
		return err
	}
	return c.postForm(ctx, "/rename", url.Values{
		"path": {p.String()},
		"name": {newName},
	})
}

// Move moves a file to a new path.
func (c *Client) Move(ctx context.Context, from, to Path) error {
	if err := checkWritable(from); err != nil {
		return err
	}
	if err := checkWritable(to); err != nil {
		return err
	}
	return c.postForm(ctx, "/move", url.Values{
		"path": {from.String()},
		"dest": {to.String()},
	})
}

// Delete removes a file. The firmware refuses non-empty directories.
func (c *Client) Delete(ctx context.Context, p Path) error {
	if err := checkWritable(p); err != nil {
		return err
	}
	return c.postForm(ctx, "/delete", url.Values{"path": {p.String()}})
}

// DeleteMany removes several paths in one request.
func (c *Client) DeleteMany(ctx context.Context, paths []Path) error {
	if len(paths) == 0 {
		return nil
	}
	list := make([]string, 0, len(paths))
	for _, p := range paths {
		if err := checkWritable(p); err != nil {
			return err
		}
		list = append(list, p.String())
	}

	encoded, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return c.postForm(ctx, "/delete", url.Values{"paths": {string(encoded)}})
}

// postForm submits a form-encoded request and maps the response to an error.
func (c *Client) postForm(ctx context.Context, endpoint string, values url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL(endpoint),
		strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		if devErr := ParseDeviceError(string(body)); devErr != nil {
			return devErr
		}
		return fmt.Errorf("%s returned %s", endpoint, resp.Status)
	}
	// The firmware returns 200 with an error string for some failures.
	if text := strings.TrimSpace(string(body)); strings.HasPrefix(text, "ERROR") {
		return ParseDeviceError(text)
	}
	return nil
}

// do performs a request, converting connection failures into ErrNotInTransfer.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrNotInTransfer, err)
	}
	return resp, nil
}

// checkHTTPStatus turns a non-2xx response into an error, reading the body for
// a device message.
func checkHTTPStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if devErr := ParseDeviceError(string(body)); devErr != nil {
		return devErr
	}
	return fmt.Errorf("device returned %s", resp.Status)
}
