package device

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestStatus(t *testing.T) {
	d := newFakeDevice(t)
	c := d.client()

	s, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Device != ModelX4 || s.Mode != "STA" || s.Version != TestedVersion {
		t.Errorf("status = %+v", s)
	}
	if c.CachedStatus() == nil {
		t.Error("status was not cached")
	}
}

// A device that is asleep or out of transfer mode is the most common failure,
// and it must be legible rather than surfacing as a generic timeout.
func TestStatusReportsNotInTransfer(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		// Bind and immediately close a port so nothing is listening.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()

		c := New(addr)
		err = c.Ping(context.Background())
		if !errors.Is(err, ErrNotInTransfer) {
			t.Errorf("err = %v, want ErrNotInTransfer", err)
		}
	})

	t.Run("non-200 response", func(t *testing.T) {
		d := newFakeDevice(t)
		d.failStatus = true

		err := d.client().Ping(context.Background())
		if !errors.Is(err, ErrNotInTransfer) {
			t.Errorf("err = %v, want ErrNotInTransfer", err)
		}
	})
}

func TestList(t *testing.T) {
	d := newFakeDevice(t)
	d.addFile("/Books/Melville/Moby-Dick.epub", []byte("body"))
	d.addFile("/Books/notes.txt", []byte("hello"))
	d.addDir("/Books/Empty")

	entries, err := d.client().List(context.Background(), NewPath("/Books"))
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]FileEntry{}
	for _, e := range entries {
		got[e.Name] = e
	}

	if e, ok := got["notes.txt"]; !ok || e.Size != 5 || e.IsDirectory {
		t.Errorf("notes.txt = %+v", e)
	}
	if e, ok := got["Melville"]; !ok || !e.IsDirectory {
		t.Errorf("Melville = %+v", e)
	}
	if e, ok := got["Empty"]; !ok || !e.IsDirectory {
		t.Errorf("Empty = %+v", e)
	}
}

func TestListMissingDirectory(t *testing.T) {
	d := newFakeDevice(t)
	_, err := d.client().List(context.Background(), NewPath("/NoSuchDir"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestListRecursive(t *testing.T) {
	d := newFakeDevice(t)
	d.addFile("/Books/a.epub", []byte("a"))
	d.addFile("/Books/Melville/Moby-Dick.epub", []byte("bb"))
	d.addFile("/Books/Le Guin/Earthsea/Wizard.epub", []byte("ccc"))
	// Protected areas must never be walked into.
	d.addFile("/Books/.crosspoint/epub_abc/progress.bin", []byte("secret"))

	files, err := d.client().ListRecursive(context.Background(), NewPath("/Books"))
	if err != nil {
		t.Fatal(err)
	}

	var paths []string
	for p := range files {
		paths = append(paths, p.String())
	}
	sort.Strings(paths)

	want := []string{
		"/Books/Le Guin/Earthsea/Wizard.epub",
		"/Books/Melville/Moby-Dick.epub",
		"/Books/a.epub",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("got  %v\nwant %v", paths, want)
	}
	if files[NewPath("/Books/Melville/Moby-Dick.epub")].Size != 2 {
		t.Error("sizes were not carried through")
	}
}

func TestDownload(t *testing.T) {
	d := newFakeDevice(t)
	d.addFile("/Books/a.epub", []byte("book bytes"))

	rc, err := d.client().Download(context.Background(), NewPath("/Books/a.epub"))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "book bytes" {
		t.Errorf("body = %q", body)
	}

	if _, err := d.client().Download(context.Background(), NewPath("/Books/missing.epub")); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMkdirAndDelete(t *testing.T) {
	d := newFakeDevice(t)
	c := d.client()
	ctx := context.Background()

	if err := c.Mkdir(ctx, NewPath("/Books")); err != nil {
		t.Fatal(err)
	}
	if err := c.MkdirAll(ctx, NewPath("/Books/Le Guin/Earthsea")); err != nil {
		t.Fatal(err)
	}

	entries, err := c.List(ctx, NewPath("/Books/Le Guin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "Earthsea" {
		t.Errorf("entries = %+v", entries)
	}

	// MkdirAll over an existing tree must be a no-op, not an error.
	if err := c.MkdirAll(ctx, NewPath("/Books/Le Guin/Earthsea")); err != nil {
		t.Errorf("MkdirAll on an existing path: %v", err)
	}

	if err := c.Delete(ctx, NewPath("/Books/Le Guin/Earthsea")); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteNonEmptyDirectoryFails(t *testing.T) {
	d := newFakeDevice(t)
	d.addFile("/Books/Melville/Moby-Dick.epub", []byte("x"))

	err := d.client().Delete(context.Background(), NewPath("/Books/Melville"))
	if err == nil {
		t.Fatal("expected an error deleting a non-empty directory")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("err = %v", err)
	}
}

func TestRenameAndMove(t *testing.T) {
	d := newFakeDevice(t)
	d.addFile("/Books/old.epub", []byte("x"))
	c := d.client()
	ctx := context.Background()

	if err := c.Rename(ctx, NewPath("/Books/old.epub"), "new.epub"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, NewPath("/Books/new.epub")); err != nil {
		t.Errorf("renamed file missing: %v", err)
	}

	d.addDir("/Archive")
	if err := c.Move(ctx, NewPath("/Books/new.epub"), NewPath("/Archive/new.epub")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, NewPath("/Archive/new.epub")); err != nil {
		t.Errorf("moved file missing: %v", err)
	}
}

func TestDeleteMany(t *testing.T) {
	d := newFakeDevice(t)
	d.addFile("/Books/a.epub", []byte("a"))
	d.addFile("/Books/b.epub", []byte("b"))

	err := d.client().DeleteMany(context.Background(), []Path{
		NewPath("/Books/a.epub"), NewPath("/Books/b.epub"),
	})
	if err != nil {
		t.Fatal(err)
	}

	entries, _ := d.client().List(context.Background(), NewPath("/Books"))
	if len(entries) != 0 {
		t.Errorf("entries remain: %+v", entries)
	}

	// An empty list is a no-op that must not issue a request.
	if err := d.client().DeleteMany(context.Background(), nil); err != nil {
		t.Errorf("empty DeleteMany: %v", err)
	}
}

// Every mutating call must refuse a protected path before it reaches the wire.
// The guard lives at the lowest layer so no caller can route around it.
func TestProtectedPathsAreRefused(t *testing.T) {
	d := newFakeDevice(t)
	c := d.client()
	ctx := context.Background()

	protected := []Path{
		NewPath("/.crosspoint"),
		NewPath("/.crosspoint/settings.json"),
		NewPath("/Books/.crosspoint/epub_abc/progress.bin"),
		NewPath("/System Volume Information"),
		NewPath("/System Volume Information/x"),
		NewPath("/XTCache"),
		NewPath("/XTCache/nested/file.bin"),
	}

	for _, p := range protected {
		t.Run(p.String(), func(t *testing.T) {
			if !IsProtected(p) {
				t.Fatalf("IsProtected(%s) = false", p)
			}
			for name, err := range map[string]error{
				"Delete": c.Delete(ctx, p),
				"Mkdir":  c.Mkdir(ctx, p),
				"Move":   c.Move(ctx, p, NewPath("/Books/x")),
				"Rename": c.Rename(ctx, p, "x"),
			} {
				if !errors.Is(err, ErrProtectedPath) {
					t.Errorf("%s(%s) = %v, want ErrProtectedPath", name, p, err)
				}
			}
		})
	}

	// Moving a legitimate file *into* a protected area must also be refused.
	if err := c.Move(ctx, NewPath("/Books/a.epub"), NewPath("/.crosspoint/a.epub")); !errors.Is(err, ErrProtectedPath) {
		t.Errorf("move into a protected path = %v, want ErrProtectedPath", err)
	}
	if err := c.DeleteMany(ctx, []Path{NewPath("/Books/ok.epub"), NewPath("/XTCache/x")}); !errors.Is(err, ErrProtectedPath) {
		t.Errorf("DeleteMany with a protected path = %v, want ErrProtectedPath", err)
	}
}

func TestUnprotectedPathsAreAllowed(t *testing.T) {
	for _, p := range []string{
		"/Books",
		"/Books/crosspoint.epub",       // substring, not a component
		"/Books/My XTCache Notes.epub", // substring, not a component
		"/shelf/device.json",           // shelf's own visible state
		"/Books/.hidden.epub",          // a dotfile is not protected
	} {
		if IsProtected(NewPath(p)) {
			t.Errorf("IsProtected(%s) = true, want false", p)
		}
	}
}

// The firmware returns some failures as a 200 response with an "ERROR:" body.
func TestErrorStringsMapToTypedErrors(t *testing.T) {
	tests := []struct {
		msg  string
		want error
	}{
		{"Upload already in progress", ErrUploadInProgress},
		{"ERROR:Upload already in progress", ErrUploadInProgress},
		{"Write failed - disk full?", ErrDiskFull},
		{"ERROR:Write failed - disk full?", ErrDiskFull},
		{"Invalid START format", ErrInvalidStart},
		{"Failed to create file", ErrCreateFailed},
		{"No upload in progress", ErrNoUpload},
		{"Upload overflow", ErrOverflow},
	}

	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			err := ParseDeviceError(tt.msg)
			if !errors.Is(err, tt.want) {
				t.Errorf("ParseDeviceError(%q) = %v, want %v", tt.msg, err, tt.want)
			}
			// The original text must survive for diagnosis.
			if !strings.Contains(err.Error(), strings.TrimPrefix(tt.msg, "ERROR:")) {
				t.Errorf("error text lost the device message: %v", err)
			}
		})
	}

	// An unrecognized message is reported verbatim rather than swallowed.
	err := ParseDeviceError("ERROR:Something entirely new")
	if err == nil || !strings.Contains(err.Error(), "Something entirely new") {
		t.Errorf("unknown message = %v", err)
	}
	if ParseDeviceError("") != nil || ParseDeviceError("   ") != nil {
		t.Error("an empty message must not produce an error")
	}
}

// Injected firmware errors must surface as typed errors through a real call.
func TestInjectedDeviceErrors(t *testing.T) {
	d := newFakeDevice(t)
	d.addFile("/Books/a.epub", []byte("x"))

	d.failNextPost = "Write failed - disk full?"
	err := d.client().Delete(context.Background(), NewPath("/Books/a.epub"))
	if !errors.Is(err, ErrDiskFull) {
		t.Errorf("err = %v, want ErrDiskFull", err)
	}
	if !IsFatal(err) {
		t.Error("a full disk must abort the whole run")
	}
}

func TestIsFatalAndRetryable(t *testing.T) {
	fatal := []error{ErrDiskFull, ErrNotInTransfer, ErrUnsupportedFirmware}
	for _, err := range fatal {
		if !IsFatal(err) {
			t.Errorf("IsFatal(%v) = false", err)
		}
		if IsRetryable(err) {
			t.Errorf("IsRetryable(%v) = true; a fatal error must not be retried", err)
		}
	}

	// A concurrent upload clears once the other transfer finishes.
	if !IsRetryable(ErrUploadInProgress) {
		t.Error("ErrUploadInProgress should be retryable")
	}
	// Protocol mistakes repeat identically, so retrying is pointless.
	for _, err := range []error{ErrProtectedPath, ErrInvalidStart, ErrOverflow, ErrNotFound} {
		if IsRetryable(err) {
			t.Errorf("IsRetryable(%v) = true", err)
		}
	}
	if IsRetryable(nil) {
		t.Error("IsRetryable(nil) should be false")
	}
}

func TestPathHelpers(t *testing.T) {
	tests := []struct {
		in    string
		want  string
		depth int
	}{
		{"/Books", "/Books", 1},
		{"Books", "/Books", 1},
		{"/Books/", "/Books", 1},
		{"/Books//Le Guin///x.epub", "/Books/Le Guin/x.epub", 3},
		{`\Books\Le Guin`, "/Books/Le Guin", 2},
		{"/", "/", 0},
		{"", "/", 0},
		{"/Books/../Other", "/Other", 1},
	}
	for _, tt := range tests {
		got := NewPath(tt.in)
		if got.String() != tt.want {
			t.Errorf("NewPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if got.Depth() != tt.depth {
			t.Errorf("NewPath(%q).Depth() = %d, want %d", tt.in, got.Depth(), tt.depth)
		}
	}

	p := NewPath("/Books/Le Guin/Earthsea/Wizard.epub")
	if p.Base() != "Wizard.epub" {
		t.Errorf("Base = %q", p.Base())
	}
	if p.Dir().String() != "/Books/Le Guin/Earthsea" {
		t.Errorf("Dir = %q", p.Dir())
	}
	if got := NewPath("/Books").Join("Le Guin", "x.epub").String(); got != "/Books/Le Guin/x.epub" {
		t.Errorf("Join = %q", got)
	}

	// Ancestors are shallowest-first so mkdir can create parents before children.
	var ancestors []string
	for _, a := range p.Ancestors() {
		ancestors = append(ancestors, a.String())
	}
	want := []string{"/Books", "/Books/Le Guin", "/Books/Le Guin/Earthsea"}
	if !reflect.DeepEqual(ancestors, want) {
		t.Errorf("Ancestors = %v, want %v", ancestors, want)
	}
}

func TestIsHidden(t *testing.T) {
	if !IsHidden(NewPath("/Books/.hidden.epub")) {
		t.Error("dotfile should be hidden")
	}
	if IsHidden(NewPath("/Books/visible.epub")) {
		t.Error("normal file should not be hidden")
	}
	// shelf keeps its marker in a visible directory precisely so the firmware's
	// hidden-file handling never applies to it.
	if IsHidden(NewPath("/shelf/device.json")) {
		t.Error("/shelf must not be hidden")
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in                  string
		major, minor, patch int
		wantErr             bool
	}{
		{"1.0.0", 1, 0, 0, false},
		{"v1.2.3", 1, 2, 3, false},
		{"2.1", 2, 1, 0, false},
		{"1.0.0-beta", 1, 0, 0, false},
		{"1.0.0+build7", 1, 0, 0, false},
		{"", 0, 0, 0, true},
		{"not.a.version", 0, 0, 0, true},
	}
	for _, tt := range tests {
		v, err := ParseVersion(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseVersion(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if v.Major != tt.major || v.Minor != tt.minor || v.Patch != tt.patch {
			t.Errorf("ParseVersion(%q) = %d.%d.%d", tt.in, v.Major, v.Minor, v.Patch)
		}
	}
}

// An unknown firmware major must fail loudly rather than being guessed at.
func TestCheckCompat(t *testing.T) {
	if err := CheckCompat(&Status{Version: TestedVersion}); err != nil {
		t.Errorf("the pinned version should be accepted: %v", err)
	}
	if err := CheckCompat(&Status{Version: "1.9.3"}); err != nil {
		t.Errorf("same major should be accepted: %v", err)
	}

	for _, v := range []string{"2.0.0", "0.9.0", "99.0.0"} {
		err := CheckCompat(&Status{Version: v})
		if !errors.Is(err, ErrUnsupportedFirmware) {
			t.Errorf("CheckCompat(%q) = %v, want ErrUnsupportedFirmware", v, err)
		}
		if !IsFatal(err) {
			t.Errorf("an unsupported firmware must abort the run")
		}
	}

	if err := CheckCompat(nil); !errors.Is(err, ErrUnsupportedFirmware) {
		t.Errorf("nil status = %v", err)
	}

	if w := CompatWarning(&Status{Version: TestedVersion}); w != "" {
		t.Errorf("no warning expected for the pinned version, got %q", w)
	}
	if w := CompatWarning(&Status{Version: "1.4.0"}); w == "" {
		t.Error("expected a warning for a different patch version")
	}
}

func TestKnownModel(t *testing.T) {
	if !KnownModel(ModelX3) || !KnownModel(ModelX4) {
		t.Error("X3 and X4 should be known")
	}
	if KnownModel("X9") {
		t.Error("X9 should be unknown")
	}
}

func TestNormalizeHost(t *testing.T) {
	tests := []struct{ in, want string }{
		{"crosspoint.local", "crosspoint.local"},
		{"http://crosspoint.local", "crosspoint.local"},
		{"http://crosspoint.local/", "crosspoint.local"},
		{"ws://192.168.1.42", "192.168.1.42"},
		{"192.168.1.42:8080", "192.168.1.42:8080"},
		{"  crosspoint.local  ", "crosspoint.local"},
	}
	for _, tt := range tests {
		if got := normalizeHost(tt.in); got != tt.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestURLConstruction(t *testing.T) {
	c := New("crosspoint.local")
	if got := c.baseURL("/api/status"); got != "http://crosspoint.local:80/api/status" {
		t.Errorf("baseURL = %q", got)
	}
	if got := c.wsURL(); got != "ws://crosspoint.local:81/" {
		t.Errorf("wsURL = %q", got)
	}

	// An explicit HTTP port must not leak into the WebSocket URL, which always
	// uses port 81.
	c2 := New("192.168.1.42:8080")
	if got := c2.baseURL("/x"); got != "http://192.168.1.42:8080/x" {
		t.Errorf("baseURL = %q", got)
	}
	if got := c2.wsURL(); got != "ws://192.168.1.42:81/" {
		t.Errorf("wsURL = %q", got)
	}
}

// Paths with spaces and non-ASCII characters must survive URL encoding.
func TestPathsAreURLEncoded(t *testing.T) {
	d := newFakeDevice(t)
	d.addFile("/Books/Ursula K. Le Guin/Earthsea 01 - A Wizard.epub", []byte("x"))
	d.addFile("/Books/村上 春樹/海辺のカフカ.epub", []byte("yy"))

	c := d.client()
	ctx := context.Background()

	for _, p := range []string{
		"/Books/Ursula K. Le Guin/Earthsea 01 - A Wizard.epub",
		"/Books/村上 春樹/海辺のカフカ.epub",
	} {
		rc, err := c.Download(ctx, NewPath(p))
		if err != nil {
			t.Errorf("Download(%s): %v", p, err)
			continue
		}
		rc.Close()
	}
}

func TestContextCancellation(t *testing.T) {
	d := newFakeDevice(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := d.client().Status(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestParseDiscoveryReply(t *testing.T) {
	addr := &net.UDPAddr{IP: net.ParseIP("192.168.1.42"), Port: 8134}

	tests := []struct {
		reply    string
		wantHost string
		wantPort int
		ok       bool
	}{
		{"crosspoint (on crosspoint-abc123);81", "crosspoint-abc123", 81, true},
		{"crosspoint (on my-reader);81\n", "my-reader", 81, true},
		{"crosspoint (on x);8080", "x", 8080, true},
		{"something else", "", 0, false},
		{"", "", 0, false},
	}

	for _, tt := range tests {
		got, ok := parseReply(tt.reply, addr)
		if ok != tt.ok {
			t.Errorf("parseReply(%q) ok = %v, want %v", tt.reply, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.Hostname != tt.wantHost || got.WSPort != tt.wantPort {
			t.Errorf("parseReply(%q) = %+v", tt.reply, got)
		}
		if got.Addr != "192.168.1.42" {
			t.Errorf("Addr = %q", got.Addr)
		}
	}
}

func TestBroadcastOf(t *testing.T) {
	tests := []struct {
		cidr string
		want string
	}{
		{"192.168.1.42/24", "192.168.1.255"},
		{"10.0.0.5/8", "10.255.255.255"},
		{"172.16.5.4/16", "172.16.255.255"},
	}
	for _, tt := range tests {
		_, n, err := net.ParseCIDR(tt.cidr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = n.IP.To4()
		if got := broadcastOf(n); got != tt.want {
			t.Errorf("broadcastOf(%s) = %q, want %q", tt.cidr, got, tt.want)
		}
	}
}

// Discovery must enumerate interfaces rather than relying on a single global
// broadcast, which macOS drops when the route is ambiguous.
func TestBroadcastAddrsEnumeratesInterfaces(t *testing.T) {
	addrs, err := broadcastAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if net.ParseIP(a) == nil {
			t.Errorf("%q is not a valid IP", a)
		}
	}
	t.Logf("broadcast addresses on this host: %v", addrs)
}
