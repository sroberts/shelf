package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/sroberts/shelf/internal/epub"
	"github.com/sroberts/shelf/internal/library"
)

// --- scan ---

var cmdScan = &command{
	name:    "scan",
	summary: "index the library directory",
	usage:   "scan [--deep] [--covers] [--quiet] [PATH]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("scan")
		deep := fs.Bool("deep", false, "rehash and re-read metadata for every file")
		covers := fs.Bool("covers", false, "extract cover thumbnails into the index")
		quiet := fs.Bool("quiet", false, "print only the summary")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		cfg, err := a.config()
		if err != nil {
			return err
		}
		root := cfg.LibraryRoot
		if fs.NArg() > 0 {
			root = fs.Arg(0)
		}

		db, err := a.index()
		if err != nil {
			return err
		}

		opts := library.ScanOptions{Deep: *deep, Covers: *covers}
		if !*quiet {
			opts.Progress = func(p library.ScanProgress) {
				switch p.Action {
				case library.ActionAdded, library.ActionUpdated, library.ActionRemoved:
					fmt.Printf("%-9s %s\n", p.Action, p.Path)
				case library.ActionFailed:
					fmt.Fprintf(os.Stderr, "%-9s %s: %v\n", p.Action, p.Path, p.Err)
				}
			}
		}

		res, err := db.Scan(ctx, root, opts)
		if err != nil {
			return err
		}

		fmt.Printf("\n%s: %d added, %d updated, %d unchanged, %d removed, %d skipped\n",
			root, res.Added, res.Updated, res.Unchanged, res.Removed, res.Skipped)

		if n := len(res.Errors); n > 0 {
			fmt.Fprintf(os.Stderr, "%d file(s) could not be indexed\n", n)
		}
		return nil
	},
}

// --- ls ---

var cmdLs = &command{
	name:    "ls",
	summary: "list and search books",
	usage:   "ls [--json] [--sort KEY] [--limit N] [QUERY]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("ls")
		asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
		sortBy := fs.String("sort", "", "sort key: title, author, series, size, added, modified, path, format (suffix -desc to reverse)")
		limit := fs.Int("limit", 0, "maximum number of results")
		paths := fs.Bool("paths", false, "print only file paths")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		db, err := a.index()
		if err != nil {
			return err
		}

		query := strings.Join(fs.Args(), " ")
		books, err := db.Search(query, library.SearchOptions{
			OrderBy: *sortBy,
			Limit:   *limit,
		})
		if err != nil {
			return err
		}

		switch {
		case *asJSON:
			for _, b := range books {
				if err := emitJSON(toJSON(b)); err != nil {
					return err
				}
			}
		case *paths:
			for _, b := range books {
				fmt.Println(b.Path)
			}
		default:
			printBookTable(books)
		}
		return nil
	},
}

func printBookTable(books []*library.Book) {
	if len(books) == 0 {
		fmt.Fprintln(os.Stderr, "no books match")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TITLE\tAUTHOR\tSERIES\tFORMAT\tSIZE")

	for _, b := range books {
		series := b.Series
		if series != "" && b.SeriesIndex != 0 {
			series = fmt.Sprintf("%s #%s", series,
				strconv.FormatFloat(b.SeriesIndex, 'f', -1, 64))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			truncate(b.DisplayTitle(), 50),
			truncate(b.DisplayAuthor(), 28),
			truncate(series, 24),
			b.Format,
			humanSize(b.Size))
	}
	w.Flush()
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

// --- meta ---

var cmdMeta = &command{
	name:    "meta",
	summary: "show or edit a book's metadata",
	usage:   "meta BOOK [--set FIELD=VALUE]... [--json]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("meta")
		var sets multiFlag
		fs.Var(&sets, "set", "set a field: title, author, series, series_index, language, publisher, date, tags (repeatable)")
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("%w: expected exactly one book", errUsage)
		}

		db, err := a.index()
		if err != nil {
			return err
		}

		book, err := resolveBook(db, fs.Arg(0))
		if err != nil {
			return err
		}

		if len(sets) == 0 {
			if *asJSON {
				return emitJSON(toJSON(book))
			}
			printMetadata(book)
			return nil
		}

		if book.Format != library.FormatEPUB {
			return fmt.Errorf("metadata editing is only supported for EPUB, not %s", book.Format)
		}

		patch, err := buildPatch(sets)
		if err != nil {
			return err
		}

		// Write to the file first. The index is derived, so it is refreshed
		// from the file rather than being updated in parallel with it.
		if err := epub.Write(book.Path, patch); err != nil {
			return err
		}
		if err := rescanOne(db, book.Path); err != nil {
			return err
		}

		updated, err := db.ByPath(book.Path)
		if err != nil {
			return err
		}
		if *asJSON {
			return emitJSON(toJSON(updated))
		}
		printMetadata(updated)
		return nil
	},
}

// buildPatch converts --set arguments into an epub.Patch.
func buildPatch(sets multiFlag) (epub.Patch, error) {
	var p epub.Patch

	for _, s := range sets {
		key, value, ok := strings.Cut(s, "=")
		if !ok {
			return p, fmt.Errorf("%w: --set expects FIELD=VALUE, got %q", errUsage, s)
		}
		key = strings.ToLower(strings.TrimSpace(key))

		switch key {
		case "title":
			p.Title = &value
		case "title_sort", "titlesort":
			p.TitleSort = &value
		case "language", "lang":
			p.Language = &value
		case "publisher":
			p.Publisher = &value
		case "date", "pubdate":
			p.Date = &value
		case "description":
			p.Description = &value
		case "series":
			p.Series = &value

		case "series_index", "index":
			if strings.TrimSpace(value) == "" {
				return p, fmt.Errorf("series_index requires a number")
			}
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return p, fmt.Errorf("series_index %q is not a number", value)
			}
			p.SeriesIndex = &f

		case "author", "authors":
			var authors []epub.Creator
			for _, name := range splitList(value) {
				authors = append(authors, epub.Creator{Name: name})
			}
			p.Authors = &authors

		case "tags", "subjects":
			tags := splitList(value)
			p.Subjects = &tags

		default:
			return p, fmt.Errorf("%w: unknown field %q", errUsage, key)
		}
	}
	return p, nil
}

// splitList parses a comma-separated value, dropping empty entries so that
// `--set tags=` clears the list.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func printMetadata(b *library.Book) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "%s\t%s\n", k, v)
		}
	}

	row("path", b.Path)
	row("title", b.DisplayTitle())
	row("author", b.DisplayAuthor())
	row("author sort", b.AuthorSort)
	if b.Series != "" {
		row("series", fmt.Sprintf("%s #%s", b.Series,
			strconv.FormatFloat(b.SeriesIndex, 'f', -1, 64)))
	}
	row("format", string(b.Format))
	row("size", humanSize(b.Size))
	row("language", b.Language)
	row("publisher", b.Publisher)
	row("published", b.PubDate)
	row("tags", strings.Join(b.Tags, ", "))
	for scheme, value := range b.Identifiers {
		row(scheme, value)
	}
	row("sha256", b.SHA256)
	w.Flush()
}

// resolveBook finds a book by path or by a unique title match, so the CLI
// accepts both `shelf meta ~/Books/x.epub` and `shelf meta earthsea`.
func resolveBook(db *library.DB, ref string) (*library.Book, error) {
	if abs, err := filepath.Abs(ref); err == nil {
		if b, err := db.ByPath(abs); err == nil {
			return b, nil
		}
	}
	if b, err := db.ByPath(ref); err == nil {
		return b, nil
	}

	books, err := db.Search(ref, library.SearchOptions{Limit: 10})
	if err != nil {
		return nil, err
	}
	switch len(books) {
	case 0:
		return nil, fmt.Errorf("no book matches %q", ref)
	case 1:
		return books[0], nil
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches %d books:\n", ref, len(books))
		for _, book := range books {
			fmt.Fprintf(&b, "  %s\n", book.Path)
		}
		return nil, fmt.Errorf("%s", b.String())
	}
}

// rescanOne refreshes the index entry for a single file after it changed on disk.
func rescanOne(db *library.DB, path string) error {
	_, err := db.Scan(context.Background(), filepath.Dir(path), library.ScanOptions{})
	return err
}

// --- import ---

var cmdImport = &command{
	name:    "import",
	summary: "add files to the library",
	usage:   "import [--move|--copy|--link] [--template T] [--dry-run] PATH...",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("import")
		move := fs.Bool("move", false, "move the source files")
		link := fs.Bool("link", false, "hard link instead of copying")
		tmpl := fs.String("template", "", "naming template (default: from config)")
		dryRun := fs.Bool("dry-run", false, "show destinations without changing anything")
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return fmt.Errorf("%w: no files given", errUsage)
		}
		if *move && *link {
			return fmt.Errorf("%w: --move and --link are mutually exclusive", errUsage)
		}

		cfg, err := a.config()
		if err != nil {
			return err
		}
		db, err := a.index()
		if err != nil {
			return err
		}

		mode := library.ImportCopy
		switch {
		case *move:
			mode = library.ImportMove
		case *link:
			mode = library.ImportLink
		}

		template := cfg.NamingTemplate
		if *tmpl != "" {
			template = *tmpl
		}

		results, err := db.Import(ctx, cfg.LibraryRoot, fs.Args(), library.ImportOptions{
			Mode:     mode,
			Template: template,
			DryRun:   *dryRun,
		})
		if err != nil {
			return err
		}

		var failed int
		for _, r := range results {
			if r.Err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "skipped %s: %v\n", r.Source, r.Err)
				continue
			}
			fmt.Printf("%s -> %s\n", r.Source, r.Dest)
		}

		if *dryRun {
			return nil
		}
		// Index what was just imported so `shelf ls` reflects it immediately.
		if _, err := db.Scan(ctx, cfg.LibraryRoot, library.ScanOptions{}); err != nil {
			return err
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d file(s) could not be imported", failed, len(results))
		}
		return nil
	},
}

// --- tags ---

var cmdTags = &command{
	name:    "tags",
	summary: "list tags with book counts",
	usage:   "tags [--json]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("tags")
		asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		db, err := a.index()
		if err != nil {
			return err
		}
		tags, err := db.Tags()
		if err != nil {
			return err
		}

		if *asJSON {
			for _, t := range tags {
				if err := emitJSON(t); err != nil {
					return err
				}
			}
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, t := range tags {
			fmt.Fprintf(w, "%s\t%d\n", t.Tag, t.Count)
		}
		return w.Flush()
	},
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
