package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/convert"
	"github.com/sroberts/shelf/internal/device"
	"github.com/sroberts/shelf/internal/library"
	syncpkg "github.com/sroberts/shelf/internal/sync"
)

// syncPhase is where the sync screen is in its flow.
type syncPhase int

const (
	phaseIdle syncPhase = iota
	phasePlanning
	phaseConfirm
	phaseRunning
	phaseDone
)

// syncModel previews a plan, takes confirmation, then shows live progress.
//
// The plan is built by the same pure function the CLI uses, so what is shown
// here is exactly what will be executed.
type syncModel struct {
	app    *App
	keys   KeyMap
	styles Styles

	phase syncPhase
	books []*library.Book

	dev      config.Device
	client   *device.Client
	manifest *syncpkg.Manifest
	plan     syncpkg.Plan

	// Live progress.
	bar         progress.Model
	currentOp   string
	currentSent int64
	currentSize int64
	completed   int
	failed      int
	bytesSent   int64
	events      []string

	result *syncpkg.Result
	err    error

	// stopped is closed by the executor goroutine when it exits, so a quit can
	// wait for the current upload rather than killing it mid-write.
	//
	// A channel, deliberately, and no mutex anywhere in this struct: Bubble Tea
	// passes and returns models by value, so an embedded lock would be copied
	// on every Update and protect nothing. All cross-goroutine communication
	// here goes through channels drained by a tea.Cmd.
	stopped chan struct{}

	width, height int
}

func newSyncModel(app *App, keys KeyMap, styles Styles) syncModel {
	bar := progress.New(progress.WithDefaultGradient())
	bar.Width = 40
	return syncModel{app: app, keys: keys, styles: styles, bar: bar}
}

// Messages.
type (
	planRequestMsg struct{ books []*library.Book }

	planReadyMsg struct {
		dev      config.Device
		client   *device.Client
		manifest *syncpkg.Manifest
		plan     syncpkg.Plan
	}

	syncEventMsg syncpkg.Event

	syncFinishedMsg struct {
		result *syncpkg.Result
		err    error
	}

	syncStoppedMsg struct{}
)

func (m syncModel) running() bool   { return m.phase == phaseRunning }
func (m syncModel) capturing() bool { return m.phase == phaseConfirm }

func (m *syncModel) setSize(w, h int) {
	m.width, m.height = w, h
	m.bar.Width = clamp(w-20, 10, 60)
}

// buildPlan resolves the device, reads its listing, and runs the planner.
// All of it off the UI thread.
func (m syncModel) buildPlan(ctx context.Context, books []*library.Book) tea.Cmd {
	app := m.app

	return func() tea.Msg {
		cfg := app.Config

		dev, err := cfg.DeviceByName("")
		if err != nil {
			return errMsg{err}
		}
		if dev.Transport == config.TransportSD {
			return errMsg{fmt.Errorf("the SD transport is not implemented yet")}
		}

		host := dev.Host
		if host == "" {
			found, derr := device.Discover(ctx, 4*time.Second)
			if derr != nil || len(found) == 0 {
				return errMsg{fmt.Errorf("%w: no device configured or discovered",
					device.ErrNotInTransfer)}
			}
			host = found[0].Host()
		}

		client := device.New(host)
		status, err := client.Status(ctx)
		if err != nil {
			return errMsg{err}
		}
		if err := device.CheckCompat(status); err != nil {
			return errMsg{err}
		}

		root := device.NewPath(dev.Root)
		manifestPath := cfg.Paths.DeviceStateFile(dev.Nickname)
		manifest, err := syncpkg.LoadManifest(manifestPath, dev.Nickname, root.String())
		if err != nil {
			return errMsg{err}
		}
		manifest.Model, manifest.Firmware = status.Device, status.Version

		deviceFiles, err := client.ListRecursive(ctx, root)
		if err != nil {
			return errMsg{err}
		}

		candidates, err := toCandidates(books, cfg.NamingTemplate)
		if err != nil {
			return errMsg{err}
		}

		// Conversion and optimization happen here, inside the tea.Cmd, for the
		// same reason the device listing above does: this goroutine is not the
		// Elm loop, so blocking is safe. A large PDF can take minutes and the
		// view shows no progress while it runs, which is a gap worth closing
		// once the executor's event channel is generalized to carry it.
		prepared, err := prepareBooks(ctx, cfg, dev, status.Device, candidates)
		if err != nil {
			return errMsg{err}
		}
		local := prepared.Books

		plan := syncpkg.Build(syncpkg.Input{
			Root:     root,
			Local:    local,
			Device:   deviceFiles,
			Manifest: manifest,
			Options:  syncpkg.Options{Prune: cfg.Sync.Prune},
		})

		return planReadyMsg{dev: dev, client: client, manifest: manifest, plan: plan}
	}
}

// toCandidates renders each book's device-relative destination.
//
// The extension is left off: a PDF becomes an EPUB on the way to the device,
// and pinning it at a .pdf path would be wrong.
func toCandidates(books []*library.Book, template string) ([]syncpkg.Candidate, error) {
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

// prepareBooks runs the same conversion and optimization pipeline the CLI uses.
func prepareBooks(ctx context.Context, cfg *config.Config, dev config.Device,
	model string, candidates []syncpkg.Candidate) (*syncpkg.Prepared, error) {

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
		profile, err := syncProfile(dev.Profile, model)
		if err != nil {
			return nil, err
		}
		pc.Profile = profile
	}
	return syncpkg.NewPreparer(pc).Prepare(ctx, candidates)
}

// syncProfile resolves the optimization target, preferring an explicit config
// value over the model the device reports.
func syncProfile(configured, model string) (convert.Profile, error) {
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

// execute runs the plan, streaming events back into the Elm loop.
//
// The executor runs on its own goroutine and events arrive through a channel
// drained by a tea.Cmd, which keeps Update free of blocking work.
func (m *syncModel) execute(ctx context.Context) tea.Cmd {
	events := make(chan syncpkg.Event, 64)
	done := make(chan syncFinishedMsg, 1)
	stopped := make(chan struct{})
	m.stopped = stopped

	cfg := m.app.Config
	client := m.client
	plan := m.plan
	manifest := m.manifest
	dev := m.dev
	log := m.app.Log

	go func() {
		defer close(stopped)
		defer close(events)

		res, err := syncpkg.Execute(ctx, client, plan, manifest, syncpkg.ExecOptions{
			InterOpDelay: cfg.Sync.InterOpDelay.Duration,
			MaxRetries:   cfg.Sync.MaxRetries,
			ChunkSize:    dev.ChunkSize,
			Events: func(e syncpkg.Event) {
				select {
				case events <- e:
				default: // never block the transfer on a slow renderer
				}
			},
		})

		syncpkg.ApplyDrops(manifest, plan.Drops)
		manifest.Touch(time.Now())
		if serr := manifest.Save(cfg.Paths.DeviceStateFile(dev.Nickname)); serr != nil {
			log.Error("could not save manifest", "err", serr)
		}
		if err == nil {
			if werr := syncpkg.WriteDeviceState(ctx, client, manifest); werr != nil {
				log.Warn("could not mirror state to the device", "err", werr)
			}
		}
		done <- syncFinishedMsg{result: res, err: err}
	}()

	return tea.Batch(
		waitForEvent(events, done),
		m.bar.Init(),
	)
}

// waitForEvent blocks on the next executor event and re-arms itself.
func waitForEvent(events <-chan syncpkg.Event, done <-chan syncFinishedMsg) tea.Cmd {
	return func() tea.Msg {
		select {
		case e, ok := <-events:
			if !ok {
				return <-done
			}
			return syncEventMsg(e)
		case f := <-done:
			return f
		}
	}
}

func (m syncModel) Update(ctx context.Context, msg tea.Msg) (syncModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.bar.Width = clamp(msg.Width-20, 10, 60)
		return m, nil

	case planRequestMsg:
		m.books = msg.books
		m.phase = phasePlanning
		m.err = nil
		m.result = nil
		// No status command here: batching one alongside buildPlan races it,
		// and a "building a plan…" line that outlives the finished plan
		// contradicts the screen. The phase drives the hint instead.
		return m, m.buildPlan(ctx, msg.books)

	case planReadyMsg:
		m.dev, m.client, m.manifest, m.plan = msg.dev, msg.client, msg.manifest, msg.plan
		if m.plan.IsEmpty() {
			m.phase = phaseDone
			return m, setStatus("nothing to do — everything is already in sync")
		}
		m.phase = phaseConfirm
		// Clear the "building a plan…" status. Leaving it up would contradict
		// the screen, which is now showing a finished plan and waiting for an
		// answer; the contextual hint takes over instead.
		return m, setStatus("")

	case syncEventMsg:
		return m.applyEvent(syncpkg.Event(msg))

	case syncFinishedMsg:
		m.phase = phaseDone
		m.result = msg.result
		m.err = msg.err
		if msg.err != nil {
			return m, reportErr(msg.err)
		}
		return m, setStatus("sync complete: %d done, %d failed, %s sent",
			msg.result.Completed, msg.result.Failed, humanSize(msg.result.BytesSent))

	case errMsg:
		if m.phase == phasePlanning || m.phase == phaseRunning {
			m.phase = phaseDone
			m.err = msg.err
		}
		return m, nil

	case progress.FrameMsg:
		bar, cmd := m.bar.Update(msg)
		if b, ok := bar.(progress.Model); ok {
			m.bar = b
		}
		return m, cmd

	case tea.KeyMsg:
		return m.updateKeys(ctx, msg)
	}
	return m, nil
}

func (m syncModel) applyEvent(e syncpkg.Event) (syncModel, tea.Cmd) {
	m.phase = phaseRunning

	switch e.Kind {
	case syncpkg.EventStart:
		m.currentOp = e.Op.String()
		m.currentSent, m.currentSize = 0, e.Size

	case syncpkg.EventProgress:
		m.currentSent, m.currentSize = e.Sent, e.Size

	case syncpkg.EventDone:
		m.completed++
		if e.Op.Kind == syncpkg.OpUpload {
			m.bytesSent += e.Op.Size
		}
		m.currentSent = m.currentSize

	case syncpkg.EventFailed:
		m.failed++
		m.appendEvent(m.styles.Error.Render("failed  " + e.Op.DevicePath.String()))
		if e.Err != nil {
			m.appendEvent("        " + m.styles.Subtle.Render(e.Err.Error()))
		}

	case syncpkg.EventRetry:
		m.appendEvent(m.styles.Warning.Render(
			fmt.Sprintf("retry %d  %s", e.Attempt, e.Op.DevicePath)))
	}

	return m, nil
}

func (m *syncModel) appendEvent(line string) {
	m.events = append(m.events, line)
	// Keep the tail bounded; a long sync should not grow memory without limit.
	if len(m.events) > 200 {
		m.events = m.events[len(m.events)-200:]
	}
}

func (m syncModel) updateKeys(ctx context.Context, msg tea.KeyMsg) (syncModel, tea.Cmd) {
	switch m.phase {
	case phaseConfirm:
		switch {
		case key.Matches(msg, m.keys.Confirm):
			// Repathing destroys reading positions, so the TUI refuses it
			// outright rather than burying consent in a keystroke. The CLI's
			// --repath --yes is the deliberate path for that.
			if len(m.plan.RepathWarnings) > 0 {
				m.phase = phaseDone
				return m, reportErr(fmt.Errorf(
					"this plan would move %d book(s) and destroy their reading positions; "+
						"run 'shelf sync --repath' from the CLI if you really mean it",
					len(m.plan.RepathWarnings)))
			}
			m.phase = phaseRunning
			m.completed, m.failed, m.bytesSent = 0, 0, 0
			m.events = nil
			cmd := (&m).execute(ctx)
			return m, cmd

		case key.Matches(msg, m.keys.Cancel):
			m.phase = phaseIdle
			return m, setStatus("sync cancelled")
		}

	case phaseDone:
		if key.Matches(msg, m.keys.Cancel) {
			m.phase = phaseIdle
			return m, nil
		}
	}
	return m, nil
}

// waitForStop blocks until the executor goroutine exits, then quits.
func (m syncModel) waitForStop() tea.Cmd {
	stopped := m.stopped
	if stopped == nil {
		return tea.Quit
	}
	return func() tea.Msg {
		<-stopped
		return tea.Quit()
	}
}

func (m syncModel) hint() string {
	switch m.phase {
	case phaseIdle:
		return "select books in the library and press s"
	case phasePlanning:
		return "building the plan…"
	case phaseConfirm:
		return "enter to start, esc to cancel"
	case phaseRunning:
		return "syncing — q finishes the current file, then quits"
	case phaseDone:
		return "esc to clear"
	}
	return ""
}

func (m syncModel) View() string {
	switch m.phase {
	case phaseIdle:
		// The status line already says what to do next, so this stays short
		// rather than repeating it two lines apart.
		return m.styles.Subtle.Render("No sync in progress.")
	case phasePlanning:
		return m.styles.Subtle.Render("building the plan…")
	case phaseConfirm:
		return m.viewPlan()
	case phaseRunning:
		return m.viewProgress()
	case phaseDone:
		return m.viewResult()
	}
	return ""
}

func (m syncModel) viewPlan() string {
	var b strings.Builder
	mkdirs, uploads, deletes, moves := m.plan.Counts()

	b.WriteString(m.styles.Title.Render(fmt.Sprintf("Plan for %s", m.dev.Nickname)))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("  %s uploads   %s   %s dirs   %s deletes   %s moves\n",
		m.styles.Accent.Render(fmt.Sprint(uploads)),
		m.styles.Accent.Render(humanSize(m.plan.TotalBytes)),
		m.styles.Subtle.Render(fmt.Sprint(mkdirs)),
		m.styles.Subtle.Render(fmt.Sprint(deletes)),
		m.styles.Subtle.Render(fmt.Sprint(moves))))

	if m.plan.Unchanged > 0 {
		b.WriteString(m.styles.Subtle.Render(
			fmt.Sprintf("  %d already in sync\n", m.plan.Unchanged)))
	}
	b.WriteString("\n")

	// Show the operations that will run, bounded to the visible area.
	limit := m.height - 10
	if limit < 3 {
		limit = 3
	}
	for i, op := range m.plan.Ops {
		if i >= limit {
			b.WriteString(m.styles.Subtle.Render(
				fmt.Sprintf("  … and %d more\n", len(m.plan.Ops)-limit)))
			break
		}
		b.WriteString("  " + op.String() + "\n")
	}

	if len(m.plan.RepathWarnings) > 0 {
		b.WriteString("\n")
		b.WriteString(m.styles.Error.Render(fmt.Sprintf(
			"⚠ %d book(s) would MOVE and lose their reading position.\n"+
				"  The TUI will not do this. Use 'shelf sync --repath' if you mean it.",
			len(m.plan.RepathWarnings))))
		b.WriteString("\n")
	}
	for _, w := range m.plan.Warnings {
		b.WriteString(m.styles.Warning.Render("  ! "+w) + "\n")
	}

	return b.String()
}

func (m syncModel) viewProgress() string {
	var b strings.Builder

	total := len(m.plan.Ops)
	b.WriteString(m.styles.Title.Render(
		fmt.Sprintf("Syncing to %s", m.dev.Nickname)))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("  %d/%d operations   %s sent\n",
		m.completed, total, humanSize(m.bytesSent)))
	if m.failed > 0 {
		b.WriteString("  " + m.styles.Error.Render(fmt.Sprintf("%d failed", m.failed)) + "\n")
	}
	b.WriteString("\n")

	if m.currentOp != "" {
		b.WriteString("  " + truncateWidth(m.currentOp, max(20, m.width-4)) + "\n")
	}

	ratio := 0.0
	if m.currentSize > 0 {
		ratio = float64(m.currentSent) / float64(m.currentSize)
	}
	b.WriteString("  " + m.bar.ViewAs(ratio) + "\n")

	if len(m.events) > 0 {
		b.WriteString("\n")
		tail := m.events
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		for _, e := range tail {
			b.WriteString("  " + e + "\n")
		}
	}
	return b.String()
}

func (m syncModel) viewResult() string {
	var b strings.Builder

	if m.err != nil {
		b.WriteString(m.styles.Error.Render("Sync failed"))
		b.WriteString("\n\n  " + m.err.Error() + "\n")
	} else if m.result != nil {
		b.WriteString(m.styles.Success.Render("Sync complete"))
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf("  %d completed, %d failed, %s sent\n",
			m.result.Completed, m.result.Failed, humanSize(m.result.BytesSent)))
		for _, e := range m.result.Errors {
			b.WriteString("  " + m.styles.Error.Render(e.Error()) + "\n")
		}
	} else {
		b.WriteString(m.styles.Success.Render("Everything is already in sync."))
		b.WriteString("\n")
	}

	if len(m.events) > 0 {
		b.WriteString("\n")
		for _, e := range m.events {
			b.WriteString("  " + e + "\n")
		}
	}
	return b.String()
}
