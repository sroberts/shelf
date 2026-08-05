package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/sroberts/shelf/internal/kosync"
)

var cmdServe = &command{
	name:    "serve",
	summary: "run the reading-progress sync server",
	usage:   "serve [--kosync ADDR] [--no-registration] [--verbose]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("serve")
		addr := fs.String("kosync", ":8080", "address to listen on")
		noRegistration := fs.Bool("no-registration", false,
			"refuse new account creation (set this once your reader is registered)")
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

		httpSrv := &http.Server{
			Addr:              *addr,
			Handler:           srv.Handler(),
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

		printServerBanner(ln.Addr(), store.Path(), srv.AllowRegistration)

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
func printServerBanner(addr net.Addr, storePath string, registration bool) {
	port := "8080"
	if tcp, ok := addr.(*net.TCPAddr); ok {
		port = fmt.Sprint(tcp.Port)
	}

	fmt.Printf("kosync server listening on %s\n", addr)
	fmt.Printf("progress store: %s\n\n", storePath)

	hosts := lanAddresses()
	if len(hosts) == 0 {
		fmt.Println("Could not determine this machine's LAN address.")
		fmt.Println("Point the reader at http://<this-machine>:" + port)
	} else {
		fmt.Println("On the reader, set the KOReader sync server to:")
		for _, h := range hosts {
			fmt.Printf("    http://%s:%s\n", h, port)
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
