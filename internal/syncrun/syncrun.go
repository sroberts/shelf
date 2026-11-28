// Package syncrun runs a whole sync — open the device, list it, prepare the
// books, plan, execute, save the manifest — for a caller that is not a
// terminal: `shelf serve`, where the web UI and MCP start syncs.
//
// It adds no policy of its own. Every step is the one `shelf sync` takes, in
// the same order, and the planner is the same pure function, so a preview here
// describes exactly what a run does. What it leaves out is deliberate:
// repathing is never offered, because moving a pinned book destroys its
// reading position and must stay behind the CLI's explicit `--repath` and its
// list of what will be lost.
package syncrun

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/convert"
	"github.com/sroberts/shelf/internal/device"
	"github.com/sroberts/shelf/internal/library"
	syncpkg "github.com/sroberts/shelf/internal/sync"
	"github.com/sroberts/shelf/internal/target"
)

// Options are the choices a remote caller may make.
type Options struct {
	// Prune deletes device files the shelf no longer holds.
	Prune bool
	// RespectDeviceDeletes treats a book deleted on the device as intended.
	RespectDeviceDeletes bool
}

// Session is a planned sync with its device open, ready to execute once.
type Session struct {
	Device   config.Device
	Label    string // how the device is named in messages: host or mount
	Plan     syncpkg.Plan
	Notes    []string // preparation notes: conversions, optimizations, failures
	Warnings []string // compatibility warnings from the device's status

	cfg          *config.Config
	client       target.Target
	manifest     *syncpkg.Manifest
	manifestPath string
	executed     bool
}

// Prepare opens dev and plans sending books to it.
func Prepare(ctx context.Context, cfg *config.Config, dev config.Device, books []*library.Book, opts Options) (*Session, error) {
	root := device.NewPath(dev.Root)

	// The manifest is loaded before the target is opened, because an SD card
	// is verified against the device UUID it records — that is what catches a
	// card belonging to a different reader.
	manifestPath := cfg.Paths.DeviceStateFile(dev.Nickname)
	manifest, err := syncpkg.LoadManifest(manifestPath, dev.Nickname, root.String())
	if err != nil {
		return nil, err
	}

	client, label, err := target.Open(ctx, dev, manifest.DeviceUUID)
	if err != nil {
		return nil, err
	}
	s := &Session{Device: dev, Label: label, cfg: cfg, client: client, manifest: manifest, manifestPath: manifestPath}

	status, err := client.Status(ctx)
	if err != nil {
		return nil, err
	}
	if err := device.CheckCompat(status); err != nil {
		return nil, err
	}
	if w := device.CompatWarning(status); w != "" {
		s.Warnings = append(s.Warnings, w)
	}
	manifest.Model, manifest.Firmware = status.Device, status.Version

	deviceFiles, err := client.ListRecursive(ctx, root)
	if err != nil {
		return nil, err
	}

	candidates, err := Candidates(books, cfg.NamingTemplate)
	if err != nil {
		return nil, err
	}
	prep, err := Preparer(cfg, dev, status.Device)
	if err != nil {
		return nil, err
	}
	prepared, err := prep.Prepare(ctx, candidates)
	if err != nil {
		return nil, err
	}
	s.Notes = append(s.Notes, prepared.Notes...)
	if prepared.Failed > 0 {
		s.Notes = append(s.Notes, fmt.Sprintf("%d book(s) could not be prepared and will not be synced", prepared.Failed))
	}

	s.Plan = syncpkg.Build(syncpkg.Input{
		Root:     root,
		Local:    prepared.Books,
		Device:   deviceFiles,
		Manifest: manifest,
		Options: syncpkg.Options{
			Prune:                opts.Prune,
			RespectDeviceDeletes: opts.RespectDeviceDeletes,
			// Never Repath: see the package comment.
		},
	})
	return s, nil
}

// ErrExecuted is returned by a second Execute on the same session: the plan
// was computed against the device as it was, and is stale once it has run.
var ErrExecuted = errors.New("syncrun: session already executed")

// Execute runs the plan and records what was done. The manifest is saved
// whatever happens, because it is what lets a resumed sync skip the books that
// already transferred.
func (s *Session) Execute(ctx context.Context, events func(syncpkg.Event)) (*syncpkg.Result, error) {
	if s.executed {
		return nil, ErrExecuted
	}
	s.executed = true

	if s.Plan.IsEmpty() {
		// Still record any manifest entries the planner decided to forget.
		syncpkg.ApplyDrops(s.manifest, s.Plan.Drops)
		return &syncpkg.Result{}, s.manifest.Save(s.manifestPath)
	}

	res, execErr := syncpkg.Execute(ctx, s.client, s.Plan, s.manifest, syncpkg.ExecOptions{
		InterOpDelay: s.cfg.Sync.InterOpDelay.Duration,
		MaxRetries:   s.cfg.Sync.MaxRetries,
		ChunkSize:    s.Device.ChunkSize,
		Events:       events,
	})

	syncpkg.ApplyDrops(s.manifest, s.Plan.Drops)
	s.manifest.Touch(time.Now())
	saveErr := s.manifest.Save(s.manifestPath)
	if execErr != nil {
		return res, errors.Join(execErr, saveErr)
	}
	if saveErr != nil {
		return res, fmt.Errorf("could not save the manifest: %w", saveErr)
	}

	// Mirror shelf's state onto the device so another machine can pick up
	// where this one left off. A failure here loses nothing local.
	if err := syncpkg.WriteDeviceState(ctx, s.client, s.manifest); err != nil {
		s.Notes = append(s.Notes, fmt.Sprintf("could not mirror state to the device: %v", err))
	}
	return res, nil
}

// Candidates renders each book's device-relative destination.
//
// The extension is left off: a PDF becomes an EPUB on the way to the device,
// and pinning it at a .pdf path would be wrong. The template decides only a
// new book's path; the planner keeps a pinned one where the manifest says.
func Candidates(books []*library.Book, template string) ([]syncpkg.Candidate, error) {
	out := make([]syncpkg.Candidate, 0, len(books))
	for _, b := range books {
		rel, err := library.RenderTemplate(template, b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.Path, err)
		}
		out = append(out, syncpkg.Candidate{
			Path:    b.Path,
			Format:  string(b.Format),
			SHA256:  b.SHA256,
			Size:    b.Size,
			RelPath: rel,
		})
	}
	return out, nil
}

// Preparer builds the conversion and optimization pipeline the CLI uses.
func Preparer(cfg *config.Config, dev config.Device, model string) (*syncpkg.Preparer, error) {
	pc := syncpkg.PreparerConfig{
		ConvertCacheDir: filepath.Join(cfg.Paths.Cache, "converted"),
		OptimizedDir:    cfg.Paths.OptimizedDir(),
		ConverterName:   cfg.Convert.PDF,
		Timeout:         cfg.Convert.Timeout.Duration,
		Optimize:        dev.Optimize,
		Syncable: func(format string) bool {
			return library.Format(format).SyncableToDevice()
		},
	}
	if dev.Optimize {
		profile, err := Profile(dev.Profile, model)
		if err != nil {
			return nil, err
		}
		pc.Profile = profile
	}
	return syncpkg.NewPreparer(pc), nil
}

// Profile resolves the optimization target, preferring an explicit config
// value over the model the device reports.
func Profile(configured, model string) (convert.Profile, error) {
	if configured != "" {
		return convert.LookupProfile(configured)
	}
	if model != "" {
		if p, err := convert.ProfileForModel(model); err == nil {
			return p, nil
		}
	}
	return convert.LookupProfile("generic-v1")
}
