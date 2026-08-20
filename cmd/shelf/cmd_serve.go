package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/opds"
)

var cmdServe = &command{
	name:    "serve",
	summary: "run the sync and OPDS catalog server",
	usage:   "serve [--listen ADDR] [--no-registration] [--no-opds] [--open-catalog] [--verbose]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("serve")
		addr := fs.String("listen", "", "address to listen on (default: from config)")
		// Kept because it is what the flag was called before OPDS shared the
		// listener, and a flag that silently stops working is worse than one
		// that outlives its name.
		legacyAddr := fs.String("kosync", "", "deprecated alias for --listen")
		noRegistration := fs.Bool("no-registration", false,
			"refuse new account creation (set this once your reader is registered)")
		noOPDS := fs.Bool("no-opds", false, "do not serve the OPDS catalog")
		openCatalog := fs.Bool("open-catalog", false,
			"serve the OPDS catalog without a password")
		verbose := fs.Bool("verbose", false, "log every request")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		cfg, err := a.config()
		if err != nil {
			return err
		}
		if err := cfg.Paths.EnsureDirs(); err != nil {
			return err
		}

		if *addr == "" {
			*addr = *legacyAddr
		}
		if *addr == "" {
			*addr = cfg.Kosync.Listen
		}
		if *addr == "" {
			*addr = ":8080"
		}

		store, err := kosync.OpenStore(cfg.Paths.ProgressFile())
		if err != nil {
			return err
		}
		defer store.Close()

		level := slog.LevelInfo
		if *verbose {
			level = slog.LevelDebug
		}
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

		srv := kosync.NewServer(store, logger)
		srv.AllowRegistration = !*noRegistration

		// Both services share one listener so the reader needs a single
		// address for progress sync and for browsing. The mux below routes
		// /opds to the catalog and everything else to kosync, which owns the
		// root help page.
		handler := srv.Handler()
		var catalogPath string

		if !*noOPDS && !cfg.OPDS.Disabled {
			db, err := a.index()
			if err != nil {
				return err
			}

			catalog := opds.NewCatalog(db, catalogTitle(cfg))
			if cfg.OPDS.PageSize > 0 {
				catalog.PageSize = cfg.OPDS.PageSize
			}

			catalogSrv := opds.NewServer(catalog, logger)
			if !*openCatalog && !cfg.OPDS.Anonymous {
				catalogSrv.Auth = store
			}

			root := http.NewServeMux()
			root.Handle("/opds", catalogSrv.Handler())
			root.Handle("/opds/", catalogSrv.Handler())
			root.Handle("/", handler)
			handler = root
			catalogPath = "/opds"
		} else {
			// Say the catalog is off rather than letting kosync's catch-all
			// root handler answer /opds with its plain-text help page. An OPDS
			// client that asks for a feed and gets prose reports a parse
			// error, which points at the feed rather than at the switch that
			// turned it off.
			root := http.NewServeMux()
			root.HandleFunc("/opds", catalogDisabled)
			root.HandleFunc("/opds/", catalogDisabled)
			root.Handle("/", handler)
			handler = root
		}

		httpSrv := &http.Server{
			Addr:              *addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			// The device is on Wi-Fi and can be slow; these are generous but
			// still bounded, so a stalled connection cannot hold a slot open
			// indefinitely.
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
		}

		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", *addr, err)
		}

		printServerBanner(ln.Addr(), store.Path(), srv.AllowRegistration, catalogPath)

		errCh := make(chan error, 1)
		go func() {
			if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()

		select {
		case err := <-errCh:
			return err
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nshutting down…")
			// Give in-flight requests a moment. A progress write that is
			// half-committed when the process exits is a reading position
			// silently lost, and this store is the only copy.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return httpSrv.Shutdown(shutdownCtx)
		}
	},
}

// printServerBanner tells the user what to type into the reader.
//
// The device needs an address it can reach, so localhost is useless to it. The
// LAN addresses are printed explicitly because working that out by hand is the
// most likely place for someone to get stuck.
func printServerBanner(addr net.Addr, storePath string, registration bool, catalogPath string) {
	port := "8080"
	if tcp, ok := addr.(*net.TCPAddr); ok {
		port = fmt.Sprint(tcp.Port)
	}

	fmt.Printf("shelf server listening on %s\n", addr)
	fmt.Printf("progress store: %s\n", storePath)
	if catalogPath == "" {
		fmt.Println("OPDS catalog: disabled")
	}
	fmt.Println()

	hosts := lanAddresses()
	if len(hosts) == 0 {
		fmt.Println("Could not determine this machine's LAN address.")
		fmt.Println("Point the reader at http://<this-machine>:" + port)
	} else {
		fmt.Println("On the reader, set the KOReader sync server to:")
		for _, h := range hosts {
			fmt.Printf("    http://%s:%s\n", h, port)
		}
		if catalogPath != "" {
			fmt.Println("\nAnd add an OPDS catalog pointing at:")
			for _, h := range hosts {
				fmt.Printf("    http://%s:%s%s\n", h, port, catalogPath)
			}
			fmt.Println("The catalog takes the same username and password.")
		}
	}

	fmt.Println()
	if registration {
		fmt.Println("Registration is open, so the reader can create its account.")
		fmt.Println("Re-run with --no-registration once it has.")
	} else {
		fmt.Println("Registration is disabled; existing accounts only.")
	}
	fmt.Println("\nPress ctrl-c to stop.")
}

// lanAddresses lists this machine's non-loopback IPv4 addresses.
func lanAddresses() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}

// catalogTitle names the catalog the reader will see in its server list.
//
// Falls back to the library directory's own name rather than a generic "shelf",
// because the device shows this string in a list that may hold several servers
// and "shelf" tells you nothing about which machine answered.
func catalogTitle(cfg *config.Config) string {
	if cfg.OPDS.Title != "" {
		return cfg.OPDS.Title
	}
	if base := filepath.Base(cfg.LibraryRoot); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	return "shelf"
}

// catalogDisabled answers /opds when the catalog is switched off.
func catalogDisabled(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "OPDS catalog is disabled on this server", http.StatusNotFound)
}
