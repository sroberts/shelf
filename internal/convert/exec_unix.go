//go:build unix

package convert

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// runCommand executes a converter and returns its combined stderr.
//
// The child is placed in its own process group and the whole group is signalled
// on cancellation. This matters more than it looks: ebook-convert spawns
// helper processes, and killing only the parent leaves those children holding
// the output file and burning CPU long after shelf has returned. Go's
// exec.CommandContext kills just the direct child, which is not enough here.
func runCommand(ctx context.Context, bin string, args []string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stderr strings.Builder
	cmd.Stderr = &stderr
	// Converters write progress to stdout; it is noise, but a full pipe buffer
	// would deadlock the child, so it is drained into the same builder.
	cmd.Stdout = &stderr

	if err := cmd.Start(); err != nil {
		return stderr.String(), err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return stderr.String(), err

	case <-ctx.Done():
		killGroup(cmd.Process.Pid)

		// Give the group a moment to die from SIGTERM before escalating, so a
		// converter gets the chance to clean up its own temp files.
		select {
		case err := <-done:
			return stderr.String(), err
		case <-time.After(3 * time.Second):
			killGroupNow(cmd.Process.Pid)
			<-done
			return stderr.String(), ctx.Err()
		}
	}
}

// killGroup sends SIGTERM to the child's process group.
func killGroup(pid int) {
	// The negative pid addresses the group rather than the single process.
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

// killGroupNow sends SIGKILL to the child's process group.
func killGroupNow(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
