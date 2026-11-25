package main_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// freePort finds a port nothing is listening on. Outpost assigns ports in
// advance, so the test does the same rather than asking shelf to pick one.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// outpostEnv is what Outpost would hand shelf, with HOME and XDG_* pointed at
// a separate directory so anything written outside storage is caught.
func outpostEnv(t *testing.T, storage, home string, port int) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "OUTPOST_") || strings.HasPrefix(kv, "XDG_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"XDG_DATA_HOME="+filepath.Join(home, "xdg-data"),
		"OUTPOST_STORAGE_DIR="+storage,
		fmt.Sprintf("OUTPOST_PORT=%d", port),
	)
}

// TestE2EServeUnderOutpost walks the checks in the Outpost service spec §13:
// loopback bind, a healthcheck with no auth, JSON log lines with levels, state
// only in storage, a clean exit inside ten seconds of SIGTERM, and a clean
// start against a storage directory that has been replaced.
func TestE2EServeUnderOutpost(t *testing.T) {
	storage := t.TempDir()
	home := t.TempDir()
	port := freePort(t)

	cmd := exec.Command(binPath, "serve", "--scan")
	cmd.Env = outpostEnv(t, storage, home, port)
	cmd.Dir = storage // Outpost's working directory is the storage directory
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitHealthy(t, base+"/up")

	// Bound to loopback only: the same port on another local address refuses.
	if ip := firstLANAddress(); ip != "" {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, fmt.Sprint(port)), time.Second); err == nil {
			c.Close()
			t.Errorf("reachable on %s; Outpost services must bind 127.0.0.1 only", ip)
		}
	}

	// The gateway's namespace is never served.
	if resp, err := http.Get(base + "/.outpost/logout"); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("/.outpost/logout answered %d, want 404", resp.StatusCode)
		}
	}

	// The catalog still wants credentials; only /up is open.
	if resp, err := http.Get(base + "/opds"); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("/opds answered %d without credentials, want 401", resp.StatusCode)
		}
	}

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("took %v to exit after SIGTERM; Outpost sends SIGKILL at 10s", took)
	}
	if err != nil {
		t.Errorf("exit after SIGTERM: %v\nstderr: %s", err, stderr.String())
	}

	// Every log line is one JSON object with a level and a message, and none
	// carries its own timestamp — Outpost adds that.
	var msgs []string
	for line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("log line is not JSON: %q", line)
			continue
		}
		if rec["level"] == nil || rec["msg"] == nil {
			t.Errorf("log line lacks level or msg: %q", line)
		}
		if _, ok := rec["time"]; ok {
			t.Errorf("log line carries its own time: %q", line)
		}
		msgs = append(msgs, fmt.Sprint(rec["msg"]))
	}
	if !slices.Contains(msgs, "listening") || !slices.Contains(msgs, "shutting down") {
		t.Errorf("log messages %q lack listening/shutting down", msgs)
	}
	if strings.Contains(stderr.String(), "\x1b[") || strings.Contains(strings.Join(msgs, ""), "\x1b[") {
		t.Error("output carries ANSI escape sequences")
	}

	// State went to storage, and nothing at all went to HOME.
	for _, want := range []string{"data/progress.db", "data/index.db", "library"} {
		if _, err := os.Stat(filepath.Join(storage, want)); err != nil {
			t.Errorf("expected %s in storage: %v", want, err)
		}
	}
	filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != home {
			t.Errorf("wrote %s outside the storage directory", p)
		}
		return nil
	})

	// A restore replaces storage wholesale; shelf must start clean on it.
	if err := os.RemoveAll(storage); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(storage, 0o700); err != nil {
		t.Fatal(err)
	}
	again := exec.Command(binPath, "serve")
	again.Env = outpostEnv(t, storage, home, port)
	again.Dir = storage
	again.Stdout, again.Stderr = io.Discard, io.Discard
	if err := again.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Process.Kill() })
	waitHealthy(t, base+"/up")
	again.Process.Signal(syscall.SIGTERM)
	if err := again.Wait(); err != nil {
		t.Errorf("restart on restored storage: %v", err)
	}
}

// Under Outpost a missing port is fatal, non-zero, and said as a levelled
// line, rather than quietly falling back to binding every interface.
func TestE2EServeUnderOutpostWithoutAPort(t *testing.T) {
	storage := t.TempDir()
	env := outpostEnv(t, storage, t.TempDir(), 0)
	for i, kv := range env {
		if strings.HasPrefix(kv, "OUTPOST_PORT=") {
			env = append(env[:i], env[i+1:]...)
			break
		}
	}

	_, stderr, code := runShelfWithEnvOnly(t, env, "serve")
	if code == 0 {
		t.Fatal("exited 0 without a port; Outpost would treat that as a deliberate stop")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &rec); err != nil {
		t.Fatalf("error is not a JSON log line: %q", stderr)
	}
	if rec["level"] != "ERROR" || !strings.Contains(fmt.Sprint(rec["msg"]), "OUTPOST_PORT") {
		t.Errorf("got %v", rec)
	}
}

func runShelfWithEnvOnly(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), 0
}

func waitHealthy(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		start := time.Now()
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if took := time.Since(start); took > 2*time.Second {
					t.Errorf("healthcheck took %v; Outpost times out at 2s", took)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never became healthy", url)
}

func firstLANAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
			return ipn.IP.String()
		}
	}
	return ""
}
