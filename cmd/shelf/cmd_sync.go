package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/convert"
	"github.com/sroberts/shelf/internal/device"
	"github.com/sroberts/shelf/internal/library"
	syncpkg "github.com/sroberts/shelf/internal/sync"
	"github.com/sroberts/shelf/internal/target"
)

var cmdSync = &command{
	name:    "sync",
	summary: "send a shelf to a device",
	usage:   "sync [SHELF] [--device NAME] [--dry-run] [--prune] [--repath] [--yes]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("sync")
		deviceName := fs.String("device", "", "device nickname from config.toml")
		dryRun := fs.Bool("dry-run", false, "show the plan without changing the device")
		prune := fs.Bool("prune", false, "delete device files that are no longer in the shelf")
		respectDeletes := fs.Bool("respect-device-deletes", false,
			"treat a book deleted on the device as intentional instead of re-uploading it")
		repath := fs.Bool("repath", false,
			"move books whose template path changed (DESTROYS reading positions)")
		yes := fs.Bool("yes", false, "do not prompt for confirmation")
		quiet := fs.Bool("quiet", false, "print only the summary")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		cfg, err := a.config()
		if err != nil {
			return err
		}
		db, err := a.index()
		if err != nil {
			return err
		}

		dev, err := cfg.DeviceByName(*deviceName)
		if err != nil {
			return err
		}
		// Resolve which books to send.
		shelfName := fs.Arg(0)
		books, err := booksToSync(db, shelfName)
		if err != nil {
			return err
		}
		if len(books) == 0 {
			return fmt.Errorf("nothing to sync (shelf %q is empty)", orAll(shelfName))
		}

		root := device.NewPath(dev.Root)

		// The manifest is loaded before the target is opened, because an SD
		// card is verified against the device UUID it records -- that is what
		// catches a card belonging to a different reader.
		manifestPath := cfg.Paths.DeviceStateFile(dev.Nickname)
		manifest, err := syncpkg.LoadManifest(manifestPath, dev.Nickname, root.String())
		if err != nil {
			return err
		}

		client, label, err := target.Open(ctx, dev, manifest.DeviceUUID)
		if err != nil {
			return err
		}

		status, err := client.Status(ctx)
		if err != nil {
			return err
		}
		if err := device.CheckCompat(status); err != nil {
			return err
		}
		if w := device.CompatWarning(status); w != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		manifest.Model, manifest.Firmware = status.Device, status.Version

		if !*quiet {
			fmt.Fprintf(os.Stderr, "reading %s on %s...\n", root, label)
		}
		deviceFiles, err := client.ListRecursive(ctx, root)
		if err != nil {
			return err
		}

		candidates, err := syncCandidates(books, cfg.NamingTemplate)
		if err != nil {
			return err
		}

		prep, err := preparerFor(cfg, dev, status.Device)
		if err != nil {
			return err
		}
		if !*quiet {
			fmt.Fprintf(os.Stderr, "preparing %d book(s)...\n", len(candidates))
		}
		prepared, err := prep.Prepare(ctx, candidates)
		if err != nil {
			return err
		}
		for _, note := range prepared.Notes {
			fmt.Fprintf(os.Stderr, "  %s\n", note)
		}
		if prepared.Failed > 0 {
			fmt.Fprintf(os.Stderr, "%d book(s) could not be prepared and will not be synced\n",
				prepared.Failed)
		}
		local := prepared.Books

		plan := syncpkg.Build(syncpkg.Input{
			Root:     root,
			Local:    local,
			Device:   deviceFiles,
			Manifest: manifest,
			Options: syncpkg.Options{
				Prune:                *prune,
				RespectDeviceDeletes: *respectDeletes,
				Repath:               *repath,
			},
		})

		printPlan(plan, *quiet)

		if *dryRun {
			return nil
		}
		if plan.IsEmpty() {
			// Still record any manifest entries the planner decided to forget.
			syncpkg.ApplyDrops(manifest, plan.Drops)
			return manifest.Save(manifestPath)
		}

		// Repathing destroys reading positions, so it never proceeds silently.
		// The warning list is printed even under --yes: --yes suppresses the
		// prompt, not the disclosure of what is about to be lost.
		if len(plan.RepathWarnings) > 0 {
			printRepathWarning(plan)
			if !*yes && !confirmRepath() {
				return fmt.Errorf("cancelled")
			}
		}

		res, execErr := syncpkg.Execute(ctx, client, plan, manifest, syncpkg.ExecOptions{
			InterOpDelay: cfg.Sync.InterOpDelay.Duration,
			MaxRetries:   cfg.Sync.MaxRetries,
			ChunkSize:    dev.ChunkSize,
			Events:       progressPrinter(*quiet),
		})

		// Persist whatever was accomplished, even on failure: the manifest is
		// what makes a resumed sync skip the books that already transferred.
		syncpkg.ApplyDrops(manifest, plan.Drops)
		manifest.Touch(time.Now())
		if err := manifest.Save(manifestPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save the manifest: %v\n", err)
		}

		if res != nil {
			fmt.Printf("\n%d completed, %d failed, %s sent\n",
				res.Completed, res.Failed, humanSize(res.BytesSent))
			for _, e := range res.Errors {
				fmt.Fprintf(os.Stderr, "  %v\n", e)
			}
		}

		if execErr != nil {
			return execErr
		}

		// Mirror shelf's state onto the device so another machine can pick up
		// where this one left off.
		if err := syncpkg.WriteDeviceState(ctx, client, manifest); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not mirror state to the device: %v\n", err)
		}
		if res != nil && res.Failed > 0 {
			return fmt.Errorf("%d operation(s) failed", res.Failed)
		}
		return nil
	},
}

// booksToSync resolves a shelf name to its books, or the whole library.
func booksToSync(db *library.DB, shelfName string) ([]*library.Book, error) {
	if shelfName == "" {
		return db.All()
	}
	return db.ShelfBooks(shelfName)
}

func orAll(name string) string {
	if name == "" {
		return "the whole library"
	}
	return name
}

// syncCandidates renders each library book's device-relative destination from
// the naming template.
//
// The extension is left off: what a book lands as depends on whether it gets
// converted, and a PDF that becomes an EPUB must not be pinned at a .pdf path.
func syncCandidates(books []*library.Book, template string) ([]syncpkg.Candidate, error) {
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

// preparerFor assembles the conversion and optimization pipeline for a device.
//
// The panel profile comes from the model the device reports rather than from
// config alone, which is what spec.md 4 means by the status endpoint selecting
// the profile. An explicit config value still wins: a user who names a profile
// has a reason.
func preparerFor(cfg *config.Config, dev config.Device, model string) (*syncpkg.Preparer, error) {
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
		profile, err := optimizeProfile(dev.Profile, model)
		if err != nil {
			return nil, err
		}
		pc.Profile = profile
	}
	return syncpkg.NewPreparer(pc), nil
}

// optimizeProfile resolves the optimization target for a device.
func optimizeProfile(configured, model string) (convert.Profile, error) {
	if configured != "" {
		return convert.LookupProfile(configured)
	}
	if model != "" {
		if p, err := convert.ProfileForModel(model); err == nil {
			return p, nil
		}
	}
	// An unrecognized model still gets the panel-independent wins rather than
	// no optimization at all.
	return convert.LookupProfile("generic-v1")
}

// printPlan shows what a sync would do.
func printPlan(plan syncpkg.Plan, quiet bool) {
	mkdirs, uploads, deletes, moves := plan.Counts()

	if !quiet {
		for _, op := range plan.Ops {
			fmt.Printf("  %s  [%s]\n", op, op.Reason)
		}
	}

	for _, w := range plan.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if len(plan.Orphans) > 0 {
		fmt.Fprintf(os.Stderr, "%d file(s) on the device were not placed by shelf", len(plan.Orphans))
		if deletes == 0 {
			fmt.Fprint(os.Stderr, "; use --prune to remove them")
		}
		fmt.Fprintln(os.Stderr)
	}

	fmt.Printf("plan: %d mkdir, %d upload (%s), %d delete, %d move, %d unchanged\n",
		mkdirs, uploads, humanSize(plan.TotalBytes), deletes, moves, plan.Unchanged)
}

// printRepathWarning discloses exactly which books will lose their position.
func printRepathWarning(plan syncpkg.Plan) {
	fmt.Fprintf(os.Stderr, "\n--repath will MOVE %d book(s) on the device.\n",
		len(plan.RepathWarnings))
	fmt.Fprintln(os.Stderr, "Moving a book clears its render and progress cache,")
	fmt.Fprintln(os.Stderr, "so each one loses its reading position:")
	for _, w := range plan.RepathWarnings {
		fmt.Fprintf(os.Stderr, "  %s\n", w)
	}
}

// confirmRepath requires explicit consent before destroying reading positions.
func confirmRepath() bool {
	// Non-interactive callers must pass --yes rather than being prompted into
	// a hang or an accidental default.
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		fmt.Fprintln(os.Stderr, "\nrefusing to repath non-interactively; pass --yes to confirm")
		return false
	}

	fmt.Fprintf(os.Stderr, "\nType 'yes' to continue: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		// EOF means no answer was given, which is not consent.
		return false
	}
	return strings.TrimSpace(strings.ToLower(line)) == "yes"
}

// progressPrinter renders execution events to the terminal.
func progressPrinter(quiet bool) func(syncpkg.Event) {
	var lastPath device.Path

	return func(e syncpkg.Event) {
		switch e.Kind {
		case syncpkg.EventStart:
			if !quiet {
				fmt.Printf("[%d/%d] %s\n", e.Index, e.Total, e.Op)
			}
			lastPath = e.Op.DevicePath

		case syncpkg.EventProgress:
			if quiet || e.Size == 0 {
				return
			}
			// Overwrite the line in place so a large book does not scroll the
			// terminal off the screen.
			fmt.Printf("\r        %s %5.1f%%", truncate(lastPath.Base(), 40),
				float64(e.Sent)/float64(e.Size)*100)

		case syncpkg.EventDone:
			if !quiet && e.Op.Kind == syncpkg.OpUpload {
				fmt.Printf("\r        %s  done      \n", truncate(lastPath.Base(), 40))
			}

		case syncpkg.EventRetry:
			fmt.Fprintf(os.Stderr, "\n  retry %d for %s: %v\n", e.Attempt, e.Op.DevicePath, e.Err)

		case syncpkg.EventFailed:
			fmt.Fprintf(os.Stderr, "\n  FAILED %s: %v\n", e.Op.DevicePath, e.Err)
		}
	}
}

// --- push ---

var cmdPush = &command{
	name:    "push",
	summary: "upload files directly to a device path",
	usage:   "push FILE... --to /Books [--device NAME]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("push")
		deviceName := fs.String("device", "", "device nickname from config.toml")
		to := fs.String("to", "", "destination directory on the device")
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return fmt.Errorf("%w: no files given", errUsage)
		}

		cfg, err := a.config()
		if err != nil {
			return err
		}
		dev, err := cfg.DeviceByName(*deviceName)
		if err != nil {
			return err
		}

		dest := *to
		if dest == "" {
			dest = dev.Root
		}
		destPath := device.NewPath(dest)

		client, _, err := target.Open(ctx, dev, "")
		if err != nil {
			return err
		}
		if _, err := client.Status(ctx); err != nil {
			return err
		}
		if err := client.MkdirAll(ctx, destPath); err != nil {
			return err
		}

		for _, arg := range fs.Args() {
			f, err := os.Open(arg)
			if err != nil {
				return err
			}
			info, err := f.Stat()
			if err != nil {
				f.Close()
				return err
			}

			target := destPath.Join(filepath.Base(arg))
			fmt.Printf("uploading %s -> %s (%s)\n", arg, target, humanSize(info.Size()))

			err = client.Upload(ctx, target, f, info.Size(), device.UploadOptions{
				ChunkSize: dev.ChunkSize,
			})
			f.Close()
			if err != nil {
				return fmt.Errorf("upload %s: %w", arg, err)
			}
		}
		return nil
	},
}

// --- pull ---

var cmdPull = &command{
	name:    "pull",
	summary: "download files from a device",
	usage:   "pull PATH... [--out DIR] [--device NAME]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("pull")
		deviceName := fs.String("device", "", "device nickname from config.toml")
		out := fs.String("out", ".", "local directory to write into")
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return fmt.Errorf("%w: no device paths given", errUsage)
		}

		cfg, err := a.config()
		if err != nil {
			return err
		}
		dev, err := cfg.DeviceByName(*deviceName)
		if err != nil {
			return err
		}
		client, _, err := target.Open(ctx, dev, "")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(*out, 0o755); err != nil {
			return err
		}

		for _, arg := range fs.Args() {
			p := device.NewPath(arg)

			rc, err := client.Download(ctx, p)
			if err != nil {
				return err
			}

			dest := filepath.Join(*out, p.Base())
			n, err := downloadTo(dest, rc)
			if err != nil {
				return fmt.Errorf("download %s: %w", p, err)
			}
			fmt.Printf("%s -> %s (%s)\n", p, dest, humanSize(n))
		}
		return nil
	},
}

// downloadTo writes a device stream to a local file, closing both sides.
//
// Staged through a temp file and renamed only on success. A download over a
// weak link drops part way often enough to matter, and writing straight to the
// destination leaves a truncated EPUB sitting there under a plausible name —
// which the next scan indexes as a real book. An interrupted download should
// leave nothing behind.
func downloadTo(dest string, src io.ReadCloser) (int64, error) {
	defer src.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".shelf-pull-*")
	if err != nil {
		return 0, err
	}
	name := tmp.Name()

	n, copyErr := io.Copy(tmp, src)
	if copyErr != nil {
		tmp.Close()
		os.Remove(name)
		return n, copyErr
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return n, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return n, err
	}
	// os.CreateTemp makes a 0600 file; a pulled book belongs in the library at
	// the same permissions as everything else there.
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return n, err
	}
	if err := os.Rename(name, dest); err != nil {
		os.Remove(name)
		return n, err
	}
	return n, nil
}
