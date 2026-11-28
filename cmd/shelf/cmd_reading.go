package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
	"github.com/sroberts/shelf/internal/mailbox"
)

// --- progress ---

var cmdProgress = &command{
	name:    "progress",
	summary: "show or clear reading positions the reader synced",
	usage:   "progress [ls] [--user U] [--json] | progress clear BOOK... [--user U] [--yes]",
	run: func(ctx context.Context, a *app, args []string) error {
		if len(args) > 0 {
			switch args[0] {
			case "ls", "list":
				return progressList(a, args[1:])
			case "clear", "rm":
				return progressClear(a, args[1:])
			}
		}
		return progressList(a, args)
	},
}

// progressJSON is one reading position, in `shelf progress --json`.
type progressJSON struct {
	Book       bookJSON `json:"book"`
	Percentage float64  `json:"percentage"`
	Device     string   `json:"device,omitempty"`
	Updated    int64    `json:"updated_unix"`
}

func progressList(a *app, args []string) error {
	fs := newFlagSet("progress")
	user := fs.String("user", "", "whose progress (default: [kosync] user)")
	asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	who, err := a.progressUser(*user)
	if err != nil {
		return err
	}

	db, err := a.index()
	if err != nil {
		return err
	}
	store, err := a.progressStore()
	if err != nil {
		return err
	}
	defer store.Close()
	byDoc, err := store.AllProgress(who)
	if err != nil {
		return err
	}
	books, err := db.All()
	if err != nil {
		return err
	}

	type entry struct {
		book *library.Book
		p    kosync.Progress
	}
	var entries []entry
	for _, b := range books {
		if p, ok := byDoc[b.DocID]; ok && b.DocID != "" {
			entries = append(entries, entry{b, p})
		}
	}
	// Most recently read first: that is the book you are reading now.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].p.Timestamp > entries[j].p.Timestamp })

	if *asJSON {
		for _, e := range entries {
			if err := emitJSON(progressJSON{
				Book:       toJSONWithProgress(e.book, byDoc),
				Percentage: e.p.Percentage * 100,
				Device:     e.p.Device,
				Updated:    e.p.Timestamp,
			}); err != nil {
				return err
			}
		}
		return nil
	}

	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "no reading progress for %q yet\n", who)
		if len(byDoc) > 0 {
			// Positions exist but match no indexed book: the files changed
			// since they were read, or the library has not been scanned.
			fmt.Fprintf(os.Stderr, "%d position(s) match no indexed book; try 'shelf scan'\n", len(byDoc))
		}
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tTITLE\tAUTHOR\tREAD\tDEVICE\tUPDATED")
	for _, e := range entries {
		fmt.Fprintf(w, "id:%d\t%s\t%s\t%s\t%s\t%s\n", e.book.ID,
			truncate(e.book.DisplayTitle(), 44), truncate(e.book.DisplayAuthor(), 24),
			kosync.FormatPercent(e.p.Percentage*100), e.p.Device, ago(e.p.Timestamp, time.Now()))
	}
	return w.Flush()
}

func progressClear(a *app, args []string) error {
	fs := newFlagSet("progress clear")
	user := fs.String("user", "", "whose progress (default: [kosync] user)")
	yes := fs.Bool("yes", false, "do not prompt for confirmation")
	fs.BoolVar(yes, "y", false, "alias for --yes")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: expected at least one book", errUsage)
	}
	who, err := a.progressUser(*user)
	if err != nil {
		return err
	}
	db, err := a.index()
	if err != nil {
		return err
	}
	var targets []*library.Book
	for _, ref := range fs.Args() {
		b, err := resolveBook(db, ref)
		if err != nil {
			return err
		}
		if b.DocID == "" {
			return fmt.Errorf("%s has no reading-sync document id (rescan the library)", b.DisplayTitle())
		}
		targets = append(targets, b)
	}

	if !*yes {
		fmt.Fprintf(os.Stderr, "Clear %s's reading position for:\n", who)
		for _, b := range targets {
			fmt.Fprintf(os.Stderr, "  • %s\n", b.DisplayTitle())
		}
		// Said before asking, because it is the surprise: clearing is not
		// a way to move the reader back to the start.
		fmt.Fprintln(os.Stderr, "The reader keeps its own position and pushes it again the next time the book is open.")
		if !confirm("Clear?") {
			return errors.New("cancelled")
		}
	}

	store, err := a.progressStore()
	if err != nil {
		return err
	}
	defer store.Close()
	var missing int
	for _, b := range targets {
		switch err := store.DeleteProgress(who, b.DocID); {
		case errors.Is(err, kosync.ErrNoProgress):
			missing++
			fmt.Fprintf(os.Stderr, "no position recorded for %s\n", b.DisplayTitle())
		case err != nil:
			return err
		default:
			fmt.Printf("cleared %s\n", b.DisplayTitle())
		}
	}
	if missing == len(targets) {
		return fmt.Errorf("nothing to clear for %q", who)
	}
	return nil
}

// ago renders a Unix time as a short relative age.
func ago(unix int64, now time.Time) string {
	if unix <= 0 {
		return ""
	}
	d := now.Sub(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return time.Unix(unix, 0).Format("2006-01-02")
	}
}

// --- genres ---

var cmdGenres = &command{
	name:    "genres",
	summary: "list AI-assigned genres with book counts",
	usage:   "genres [--json]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("genres")
		asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		db, err := a.index()
		if err != nil {
			return err
		}
		counts, err := genreCounts(db, a.genres())
		if err != nil {
			return err
		}
		if *asJSON {
			for _, c := range counts {
				if err := emitJSON(c); err != nil {
					return err
				}
			}
			return nil
		}
		if len(counts) == 0 {
			fmt.Fprintln(os.Stderr, "no genres yet: 'shelf serve' classifies books when Outpost gives it an AI model")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, c := range counts {
			fmt.Fprintf(w, "%s\t%d\n", c.Genre, c.Count)
		}
		return w.Flush()
	},
}

type genreCount struct {
	Genre string `json:"genre"`
	Count int    `json:"count"`
}

// genreCounts counts only books still indexed: ai.db outlives deletions, so
// its own rows would count books that are gone.
func genreCounts(db *library.DB, genres map[string]string) ([]genreCount, error) {
	if len(genres) == 0 {
		return nil, nil
	}
	books, err := db.All()
	if err != nil {
		return nil, err
	}
	m := map[string]int{}
	for _, b := range books {
		if g := genres[b.SHA256]; g != "" {
			m[g]++
		}
	}
	out := make([]genreCount, 0, len(m))
	for g, n := range m {
		out = append(out, genreCount{g, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Genre < out[j].Genre
	})
	return out, nil
}

// --- stats ---

var cmdStats = &command{
	name:    "stats",
	summary: "summarise the library: books, authors, series, size, formats",
	usage:   "stats [--json]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("stats")
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		db, err := a.index()
		if err != nil {
			return err
		}
		st, err := db.Stats()
		if err != nil {
			return err
		}
		counts, err := genreCounts(db, a.genres())
		if err != nil {
			return err
		}
		shelves, err := db.ShelfNames()
		if err != nil {
			return err
		}

		if *asJSON {
			genres := map[string]int{}
			for _, c := range counts {
				genres[c.Genre] = c.Count
			}
			// The same shape as GET /v1/stats, so a script can switch
			// between the CLI and the API without remapping fields.
			return emitJSON(struct {
				library.Stats
				Shelves int            `json:"shelves"`
				Genres  map[string]int `json:"genres,omitempty"`
				Version string         `json:"version"`
			}{st, len(shelves), genres, readVersion().String()})
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "books\t%d\n", st.Books)
		fmt.Fprintf(w, "authors\t%d\n", st.Authors)
		fmt.Fprintf(w, "series\t%d\n", st.Series)
		fmt.Fprintf(w, "tags\t%d\n", st.Tags)
		fmt.Fprintf(w, "shelves\t%d\n", len(shelves))
		fmt.Fprintf(w, "size\t%s\n", humanSize(st.Bytes))
		formats := make([]string, 0, len(st.Formats))
		for f, n := range st.Formats {
			formats = append(formats, fmt.Sprintf("%s %d", f, n))
		}
		sort.Strings(formats)
		fmt.Fprintf(w, "formats\t%s\n", strings.Join(formats, ", "))
		if len(counts) > 0 {
			fmt.Fprintf(w, "genres\t%d\n", len(counts))
		}
		return w.Flush()
	},
}

// --- mail ---

var cmdMail = &command{
	name:    "mail",
	summary: "show the state of newsletter import from Outpost mail",
	usage:   "mail [--json]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("mail")
		asJSON := fs.Bool("json", false, "emit JSON")
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

		st := mailStatus{
			// Mail is imported by shelf serve, whose environment Outpost
			// sets; this process sees the variables only if run inside it.
			Configured: mailbox.FromEnv() != nil,
			Folder:     filepath.Join(cfg.LibraryRoot, "Mail"),
		}
		if data, err := os.ReadFile(cfg.Paths.MailStateFile()); err == nil {
			var c struct {
				After int64 `json:"after"`
			}
			if json.Unmarshal(data, &c) == nil {
				st.Cursor = c.After
			}
			if info, err := os.Stat(cfg.Paths.MailStateFile()); err == nil {
				st.LastImportUnix = info.ModTime().Unix()
			}
		}
		books, err := db.All()
		if err != nil {
			return err
		}
		prefix := st.Folder + string(filepath.Separator)
		for _, b := range books {
			if strings.HasPrefix(b.Path, prefix) {
				st.Books++
			}
		}

		if *asJSON {
			return emitJSON(st)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		state := "off: shelf serve imports mail only when Outpost gives it a mailbox (OUTPOST_MAIL_URL)"
		if st.Configured {
			state = "available in this environment"
		}
		fmt.Fprintf(w, "mail\t%s\n", state)
		fmt.Fprintf(w, "folder\t%s\n", st.Folder)
		fmt.Fprintf(w, "imported books\t%d\n", st.Books)
		if st.Cursor > 0 {
			fmt.Fprintf(w, "last message\t#%d, %s\n", st.Cursor, ago(st.LastImportUnix, time.Now()))
		} else {
			fmt.Fprintf(w, "last message\tnone imported yet\n")
		}
		return w.Flush()
	},
}

type mailStatus struct {
	Configured     bool   `json:"configured"`
	Folder         string `json:"folder"`
	Books          int    `json:"books"`
	Cursor         int64  `json:"cursor,omitempty"`
	LastImportUnix int64  `json:"last_import_unix,omitempty"`
}
