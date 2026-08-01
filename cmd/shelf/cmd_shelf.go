package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"text/tabwriter"

	"github.com/sroberts/shelf/internal/library"
)

var cmdShelf = &command{
	name:    "shelf",
	summary: "manage shelves (saved queries or explicit sets)",
	usage:   "shelf create|ls|rm|show|add|remove ...",
	run: func(ctx context.Context, a *app, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("%w: expected a subcommand (create, ls, rm, show, add, remove)", errUsage)
		}

		switch args[0] {
		case "create":
			return shelfCreate(a, args[1:])
		case "ls", "list":
			return shelfList(a, args[1:])
		case "rm", "delete":
			return shelfRemove(a, args[1:])
		case "show":
			return shelfShow(a, args[1:])
		case "add":
			return shelfAdd(a, args[1:])
		case "remove":
			return shelfRemoveBooks(a, args[1:])
		default:
			return fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
		}
	},
}

func shelfCreate(a *app, args []string) error {
	fs := newFlagSet("shelf create")
	query := fs.String("query", "", "saved search defining the shelf")
	manual := fs.Bool("manual", false, "create an explicit set instead of a saved query")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: expected a shelf name", errUsage)
	}
	if *query == "" && !*manual {
		return fmt.Errorf("%w: pass --query, or --manual for an explicit set", errUsage)
	}

	db, err := a.index()
	if err != nil {
		return err
	}

	kind := library.KindQuery
	if *manual {
		kind = library.KindManual
	}
	if err := db.CreateShelf(library.Shelf{Name: fs.Arg(0), Kind: kind, Query: *query}); err != nil {
		return err
	}
	if err := a.saveShelves(); err != nil {
		return err
	}

	fmt.Printf("created shelf %q\n", fs.Arg(0))
	return nil
}

func shelfList(a *app, args []string) error {
	fs := newFlagSet("shelf ls")
	asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	db, err := a.index()
	if err != nil {
		return err
	}
	shelves, err := db.ListShelves()
	if err != nil {
		return err
	}

	if *asJSON {
		for _, s := range shelves {
			books, _ := db.ShelfBooks(s.Name)
			if err := emitJSON(struct {
				Name  string `json:"name"`
				Kind  string `json:"kind"`
				Query string `json:"query,omitempty"`
				Books int    `json:"books"`
			}{s.Name, string(s.Kind), s.Query, len(books)}); err != nil {
				return err
			}
		}
		return nil
	}

	if len(shelves) == 0 {
		fmt.Fprintln(os.Stderr, "no shelves defined")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tBOOKS\tQUERY")
	for _, s := range shelves {
		books, _ := db.ShelfBooks(s.Name)
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", s.Name, s.Kind, len(books), s.Query)
	}
	return w.Flush()
}

func shelfRemove(a *app, args []string) error {
	fs := newFlagSet("shelf rm")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: expected a shelf name", errUsage)
	}

	db, err := a.index()
	if err != nil {
		return err
	}
	if err := db.DeleteShelf(fs.Arg(0)); err != nil {
		return err
	}
	if err := a.saveShelves(); err != nil {
		return err
	}

	fmt.Printf("deleted shelf %q\n", fs.Arg(0))
	return nil
}

func shelfShow(a *app, args []string) error {
	fs := newFlagSet("shelf show")
	asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
	paths := fs.Bool("paths", false, "print only file paths")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: expected a shelf name", errUsage)
	}

	db, err := a.index()
	if err != nil {
		return err
	}
	books, err := db.ShelfBooks(fs.Arg(0))
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
}

func shelfAdd(a *app, args []string) error {
	fs := newFlagSet("shelf add")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("%w: expected a shelf name and at least one book", errUsage)
	}

	db, err := a.index()
	if err != nil {
		return err
	}

	name := fs.Arg(0)
	paths, err := resolvePaths(db, fs.Args()[1:])
	if err != nil {
		return err
	}
	if err := db.AddToShelf(name, paths); err != nil {
		return err
	}
	if err := a.saveShelves(); err != nil {
		return err
	}

	fmt.Printf("added %d book(s) to %q\n", len(paths), name)
	return nil
}

func shelfRemoveBooks(a *app, args []string) error {
	fs := newFlagSet("shelf remove")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("%w: expected a shelf name and at least one book", errUsage)
	}

	db, err := a.index()
	if err != nil {
		return err
	}

	name := fs.Arg(0)
	paths, err := resolvePaths(db, fs.Args()[1:])
	if err != nil {
		return err
	}
	if err := db.RemoveFromShelf(name, paths); err != nil {
		return err
	}
	if err := a.saveShelves(); err != nil {
		return err
	}

	fmt.Printf("removed %d book(s) from %q\n", len(paths), name)
	return nil
}

// resolvePaths turns book references into indexed paths.
func resolvePaths(db *library.DB, refs []string) ([]string, error) {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		b, err := resolveBook(db, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, b.Path)
	}
	return out, nil
}

// --- doctor ---

var cmdDoctor = &command{
	name:    "doctor",
	summary: "check configuration, paths, and index health",
	usage:   "doctor",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("doctor")
		offline := fs.Bool("offline", false, "skip device connectivity checks")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		defer w.Flush()

		fmt.Fprintf(w, "platform\t%s/%s\n", runtime.GOOS, runtime.GOARCH)
		fmt.Fprintf(w, "go\t%s\n", runtime.Version())

		cfg, err := a.config()
		if err != nil {
			fmt.Fprintf(w, "config\tFAILED: %v\n", err)
			return err
		}

		configFile := a.configPath
		if configFile == "" {
			configFile = cfg.Paths.ConfigFile()
		}
		fmt.Fprintf(w, "config file\t%s%s\n", configFile, existsNote(configFile))
		fmt.Fprintf(w, "shelves file\t%s%s\n", cfg.Paths.ShelvesFile(), existsNote(cfg.Paths.ShelvesFile()))
		fmt.Fprintf(w, "index\t%s%s\n", cfg.Paths.IndexFile(), existsNote(cfg.Paths.IndexFile()))
		fmt.Fprintf(w, "library root\t%s%s\n", cfg.LibraryRoot, existsNote(cfg.LibraryRoot))
		fmt.Fprintf(w, "inbox\t%s%s\n", cfg.Inbox, existsNote(cfg.Inbox))
		fmt.Fprintf(w, "naming template\t%s\n", cfg.NamingTemplate)

		if len(cfg.Devices) == 0 {
			fmt.Fprintf(w, "devices\tnone configured\n")
		}
		for _, d := range cfg.Devices {
			host := d.Host
			if host == "" {
				host = "(discovery)"
			}
			fmt.Fprintf(w, "device %s\t%s transport=%s root=%s chunk=%d\n",
				d.Nickname, host, d.Transport, d.Root, d.ChunkSize)

			// Probing takes a few seconds per unreachable device, so --offline
			// exists for scripting and for checking config without hardware.
			if !*offline {
				checkDevice(ctx, w, d)
			}
		}

		db, err := a.index()
		if err != nil {
			fmt.Fprintf(w, "index status\tFAILED: %v\n", err)
			return err
		}
		n, err := db.Count()
		if err != nil {
			fmt.Fprintf(w, "index status\tFAILED: %v\n", err)
			return err
		}
		fmt.Fprintf(w, "indexed books\t%d\n", n)

		shelves, err := db.ShelfNames()
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "shelves\t%d\n", len(shelves))

		// Report duplicates rather than treating them as an error; the index
		// deliberately permits the same file at several paths.
		dupes, err := db.Duplicates()
		if err != nil {
			return err
		}
		if len(dupes) > 0 {
			fmt.Fprintf(w, "duplicate groups\t%d (run 'shelf ls --json' to inspect)\n", len(dupes))
		}
		return nil
	},
}

func existsNote(path string) string {
	if _, err := os.Stat(path); err != nil {
		return "  (missing)"
	}
	return ""
}
