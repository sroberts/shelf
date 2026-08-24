package target

import (
	"context"
	"fmt"
	"io"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/device"
	"github.com/sroberts/shelf/internal/sdcard"
	syncpkg "github.com/sroberts/shelf/internal/sync"
)

// Target is everything a command needs from a place books can go.
//
// Both a networked device and a mounted SD card satisfy it, which is what lets
// the planner, the manifest, and path pinning work identically over either.
// The interface lives here rather than in internal/sync because sync needs
// only the mutating subset; listing and downloading are the CLI's business.
// Target is a place books can go.
type Target interface {
	syncpkg.Transport
	ListRecursive(ctx context.Context, root device.Path) (map[device.Path]device.FileEntry, error)
	MkdirAll(ctx context.Context, p device.Path) error
	Download(ctx context.Context, p device.Path) (io.ReadCloser, error)
}

// Open resolves a configured device to something writable, plus a label
// naming it for messages -- a host for a network device, a mount for a card.
//
// wantUUID is the manifest's device UUID, used to catch a card that belongs to
// a different reader. Pass "" when no manifest has been loaded, which skips
// that check without weakening the others.
func Open(ctx context.Context, d config.Device, wantUUID string) (Target, string, error) {
	if d.Transport != config.TransportSD {
		c, err := NetworkClient(ctx, d)
		if err != nil {
			return nil, "", err
		}
		return c, c.Host(), nil
	}

	vol, err := sdcard.Open(d.Mount)
	if err != nil {
		return nil, "", err
	}
	// Verified before anything is written. Writing a library onto the wrong
	// volume is this transport's worst outcome, and a mistyped mount path is
	// one keystroke from a home directory.
	if err := vol.Verify(wantUUID); err != nil {
		return nil, "", err
	}
	return vol, vol.Mount(), nil
}

// Label names a device for display without opening it.
func Label(d config.Device) string {
	if d.Transport == config.TransportSD {
		if d.Mount == "" {
			return "(no mount configured)"
		}
		return d.Mount
	}
	if d.Host == "" {
		return "(discover)"
	}
	return d.Host
}

// Describe renders a one-line status suffix for a card, which cannot
// report the firmware, heap, and signal a live device does.
func Describe(d config.Device) string {
	if d.Transport == config.TransportSD {
		return fmt.Sprintf("SD card at %s", d.Mount)
	}
	return ""
}

// NetworkClient builds a client for a configured device, discovering it when
// no host is set.
//
// Exported for the paths that are network-only by nature -- doctor reads the
// signal strength and free heap, which a card has no answer for.
func NetworkClient(ctx context.Context, d config.Device) (*device.Client, error) {
	if d.Host != "" {
		return device.New(d.Host), nil
	}
	return device.Resolve(ctx, "")
}
