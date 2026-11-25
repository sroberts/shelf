package mailbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/library"
)

// Template places mail under its own top-level folder, by sender. Kept apart
// from the user's naming template so newsletters never interleave with books,
// and so a whole sender can be found, shelved or deleted in one place.
const Template = "Mail/{author}/{title}"

// pageSize is how many messages one request asks for. Each can carry 4 MiB of
// text and HTML, so this is kept well under the service's cap of 100.
const pageSize = 20

// maxSummaryInput bounds what is sent to the model: hosted models cost the
// operator money (spec §15), and a summary needs the gist, not every word.
const maxSummaryInput = 8000

// errStop marks a failure that retrying cannot fix: a wrong token.
var errStop = errors.New("mail: stopping")

// Poller imports new mail into the library.
type Poller struct {
	Client      *Client
	DB          *library.DB
	LibraryRoot string
	// StateFile holds the cursor, the id of the last message dealt with. It
	// belongs in Outpost storage: lost, it only costs a re-import that the
	// hash check turns into a no-op, but there is no reason to lose it.
	StateFile string
	// StageDir is scratch space for building an EPUB before it is imported.
	StageDir string
	// Summarize, when set, writes the book's description. Any error is
	// ignored and the message imported without one: AI is an enhancement.
	Summarize func(ctx context.Context, subject, text string) (string, error)
	Log       *slog.Logger
	// Interval between polls. Outpost checks the inbox every minute, so
	// polling faster finds nothing new.
	Interval time.Duration
	// OnImport, when set, is called after a poll that imported anything, so
	// the caller can enrich the new books without polling the index itself.
	OnImport func()

	// routes maps an alias route to its note, which names the series.
	routes map[string]string
}

// Run polls until ctx is cancelled. It never returns early except on a token
// the service refuses, which no number of retries will fix.
func (p *Poller) Run(ctx context.Context) {
	log := p.logger()
	interval := p.Interval
	if interval <= 0 {
		interval = time.Minute
	}

	ready := false
	// said remembers the last waiting state reported, so a missing account
	// is one log line rather than one a minute for as long as it is missing.
	said := ""
	note := func(state string, emit func()) {
		if state != said {
			said = state
			emit()
		}
	}
	for {
		if !ready {
			me, err := p.Client.Me(ctx)
			switch {
			case ctx.Err() != nil:
				return
			case isStatus(err, http.StatusUnauthorized):
				log.Error("mail token refused; mail import is off until shelf is restarted with a new one")
				return
			case err != nil:
				note("unavailable", func() { log.Warn("mail unavailable", "err", err) })
			case !me.Ready:
				note("no-account", func() {
					log.Info("no Gmail account is connected in Outpost; mail import waits for one")
				})
			default:
				said = ""
				ready = true
				p.setAddresses(me.Addresses)
				log.Info("mail import on", "addresses", len(me.Addresses))
			}
		}

		if ready {
			n, err := p.Poll(ctx)
			switch {
			case ctx.Err() != nil:
				return
			case errors.Is(err, errStop):
				log.Error("mail token refused; mail import is off until shelf is restarted with a new one")
				return
			case isStatus(err, http.StatusServiceUnavailable):
				// The account was disconnected; go back to waiting quietly.
				ready = false
			case err != nil:
				log.Warn("mail poll failed; retrying", "err", err)
			case n > 0:
				log.Info("mail imported", "books", n)
				if p.OnImport != nil {
					p.OnImport()
				}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Poll imports every message after the cursor, once.
func (p *Poller) Poll(ctx context.Context) (int, error) {
	after, err := p.loadCursor()
	if err != nil {
		return 0, err
	}
	if p.routes == nil {
		// Poll called on its own (tests, a one-shot): learn alias notes now.
		if me, err := p.Client.Me(ctx); err == nil {
			p.setAddresses(me.Addresses)
		} else if isStatus(err, http.StatusUnauthorized) {
			return 0, fmt.Errorf("%w: %v", errStop, err)
		}
	}

	imported := 0
	var seen map[string]bool // identifiers of mail already in the library, loaded on first need
	for {
		msgs, next, err := p.Client.Messages(ctx, after, pageSize)
		if err != nil {
			if isStatus(err, http.StatusUnauthorized) {
				return imported, fmt.Errorf("%w: %v", errStop, err)
			}
			return imported, err
		}
		if len(msgs) == 0 {
			return imported, nil
		}
		for _, m := range msgs {
			if err := ctx.Err(); err != nil {
				return imported, err
			}
			if seen == nil {
				if seen, err = p.importedMail(); err != nil {
					return imported, err
				}
			}
			added, err := p.importMessage(ctx, m, seen)
			if err != nil {
				// Stop here rather than skip: the cursor must not move past a
				// message that was never imported, or it is lost for good.
				return imported, fmt.Errorf("message %d: %w", m.ID, err)
			}
			if added {
				imported++
			}
			if err := p.saveCursor(m.ID); err != nil {
				return imported, err
			}
			after = m.ID
			// Deleted after the cursor is saved: a crash in between leaves a
			// message that is skipped by the cursor or, if the cursor file is
			// lost, re-imported as the same bytes and deduped by hash.
			if err := p.Client.Delete(ctx, m.ID); err != nil && !isStatus(err, http.StatusNotFound) {
				p.logger().Warn("could not delete imported message", "id", m.ID, "err", err)
			}
		}
		if next <= after && len(msgs) < pageSize {
			return imported, nil
		}
		if next > after {
			after = next
		}
	}
}

// importedMail collects the identifiers of mail books already indexed.
//
// The dedupe key is the message's identifier, not the file's hash: with a
// summarizer the description is model output, so the same message built twice
// is not the same bytes. Checking before building also means a re-delivered
// message is never sent to the model a second time.
func (p *Poller) importedMail() (map[string]bool, error) {
	books, err := p.DB.All()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, b := range books {
		// epub's parser files "urn:shelf:mail:X" under scheme "shelf".
		if v := b.Identifiers["shelf"]; strings.HasPrefix(v, "mail:") {
			seen[v] = true
		}
	}
	return seen, nil
}

// identifier is the book identifier for m.
func identifier(m Message) string {
	if id := strings.Trim(strings.TrimSpace(m.MessageID), "<>"); id != "" {
		return "urn:shelf:mail:" + id
	}
	return "urn:shelf:mail:" + strconv.FormatInt(m.ID, 10)
}

// importMessage builds m as an EPUB and adds it to the library. It reports
// false, with no error, when the message is already there.
func (p *Poller) importMessage(ctx context.Context, m Message, seen map[string]bool) (bool, error) {
	key := strings.TrimPrefix(identifier(m), "urn:shelf:")
	if seen[key] {
		return false, nil
	}
	book := p.bookFor(ctx, m)

	var buf bytes.Buffer
	if err := epub.WriteSimple(&buf, book); err != nil {
		return false, err
	}

	if err := os.MkdirAll(p.StageDir, 0o700); err != nil {
		return false, err
	}
	staged := filepath.Join(p.StageDir, "message-"+strconv.FormatInt(m.ID, 10)+".epub")
	if err := os.WriteFile(staged, buf.Bytes(), 0o600); err != nil {
		return false, err
	}
	defer os.Remove(staged) // a no-op once Import has moved it

	results, err := p.DB.Import(ctx, p.LibraryRoot, []string{staged}, library.ImportOptions{
		Mode: library.ImportMove, Template: Template,
	})
	if err != nil {
		return false, err
	}
	if len(results) != 1 || results[0].Err != nil {
		if len(results) == 1 {
			return false, results[0].Err
		}
		return false, errors.New("import produced no result")
	}

	// Import places the file but does not index it. Scanning just the folder
	// it landed in is cheap — unchanged files are only stat'ed — and cannot
	// remove anything outside that folder.
	if _, err := p.DB.Scan(ctx, filepath.Dir(results[0].Dest), library.ScanOptions{}); err != nil {
		return false, fmt.Errorf("index %s: %w", results[0].Dest, err)
	}
	seen[key] = true
	p.logger().Debug("mail imported", "id", m.ID, "path", results[0].Dest)
	return true, nil
}

// bookFor describes m as a book.
func (p *Poller) bookFor(ctx context.Context, m Message) epub.SimpleBook {
	when := m.Date
	if when.IsZero() {
		when = m.Received
	}
	when = when.UTC()

	subject := strings.TrimSpace(m.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	title := subject
	if !when.IsZero() {
		title = when.Format("2006-01-02") + " " + subject
	}

	author := strings.TrimSpace(m.From.Name)
	if author == "" {
		author = strings.TrimSpace(m.From.Address)
	}
	if author == "" {
		author = "Unknown sender"
	}

	series := "Mail"
	if note := p.routes[m.Route]; note != "" {
		series = note
	}

	text := bodyText(m)
	b := epub.SimpleBook{
		Title:      title,
		Authors:    []string{author},
		Date:       when,
		Series:     series,
		Subjects:   nonEmpty("mail", m.Route),
		Paragraphs: paragraphs(text),
	}
	b.Identifier = identifier(m)

	if p.Summarize != nil && strings.TrimSpace(text) != "" {
		in := text
		if len(in) > maxSummaryInput {
			in = strings.ToValidUTF8(in[:maxSummaryInput], "")
		}
		if s, err := p.Summarize(ctx, subject, in); err == nil {
			b.Description = strings.TrimSpace(s)
		} else {
			p.logger().Debug("no summary for message", "id", m.ID, "err", err)
		}
	}
	return b
}

func (p *Poller) setAddresses(addrs []Address) {
	p.routes = map[string]string{}
	for _, a := range addrs {
		if a.Alias {
			p.routes[a.Route] = strings.TrimSpace(a.Note)
		}
	}
}

type cursorFile struct {
	After int64 `json:"after"`
}

func (p *Poller) loadCursor() (int64, error) {
	data, err := os.ReadFile(p.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var c cursorFile
	if err := json.Unmarshal(data, &c); err != nil {
		// A corrupt cursor restarts from zero; the hash check keeps that from
		// duplicating anything already imported.
		p.logger().Warn("mail cursor unreadable; starting over", "path", p.StateFile, "err", err)
		return 0, nil
	}
	return c.After, nil
}

// saveCursor writes through a temp file and rename, so a crash mid-write
// leaves the old cursor rather than a truncated one.
func (p *Poller) saveCursor(after int64) error {
	data, _ := json.Marshal(cursorFile{After: after})
	dir := filepath.Dir(p.StateFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mail-cursor-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p.StateFile)
}

func (p *Poller) logger() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func isStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
