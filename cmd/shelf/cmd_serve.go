package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sroberts/shelf/internal/ai"
	"github.com/sroberts/shelf/internal/api"
	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/kosync"
	"github.com/sroberts/shelf/internal/library"
	"github.com/sroberts/shelf/internal/mailbox"
	"github.com/sroberts/shelf/internal/opds"
	"github.com/sroberts/shelf/internal/outpost"
	"github.com/sroberts/shelf/internal/ui"
)

var cmdServe = &command{
	name:    "serve",
	summary: "run the web UI, API, MCP, OPDS catalog and progress sync server",
	usage:   "serve [--listen ADDR | --port N] [--scan] [--no-registration] [--no-opds] [--open-catalog] [--no-ai] [--no-mail] [--verbose]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("serve")
		addr := fs.String("listen", "", "address to listen on (default: from config)")
		port := fs.Int("port", 0,
			"listen on 127.0.0.1:N, for running behind a proxy such as Outpost (default: $OUTPOST_PORT)")
		// Kept because it is what the flag was called before OPDS shared the
		// listener, and a flag that silently stops working is worse than one
		// that outlives its name.
		legacyAddr := fs.String("kosync", "", "deprecated alias for --listen")
		noRegistration := fs.Bool("no-registration", false,
			"refuse new account creation (set this once your reader is registered)")
		noOPDS := fs.Bool("no-opds", false, "do not serve the OPDS catalog")
		openCatalog := fs.Bool("open-catalog", false,
			"serve the OPDS catalog without a password")
		scan := fs.Bool("scan", false,
			"index the library in the background at startup, so the catalog is current")
		noAI := fs.Bool("no-ai", false,
			"do not use Outpost's AI models, even when OUTPOST_AI_URL is set")
		noMail := fs.Bool("no-mail", false,
			"do not import mail, even when OUTPOST_MAIL_URL is set")
		verbose := fs.Bool("verbose", false, "log every request")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		env := outpost.FromEnv()
		logger := serveLogger(env, *verbose)

		cfg, err := a.config()
		if err != nil {
			return err
		}
		if err := cfg.Paths.EnsureDirs(); err != nil {
			return err
		}

		listen, err := listenAddr(*addr, *legacyAddr, *port, env, cfg.Kosync.Listen)
		if err != nil {
			return err
		}

		// Opened, and migrated, before the port is bound. Outpost counts a
		// refused connection as a failed healthcheck only after the startup
		// grace, so binding last is what keeps /up from ever answering 200
		// for a server that cannot yet serve (spec §2, §14 "Updates").
		store, err := kosync.OpenStore(cfg.Paths.ProgressFile())
		if err != nil {
			return err
		}
		defer store.Close()

		srv := kosync.NewServer(store, logger)
		srv.AllowRegistration = !*noRegistration
		// One limiter for every password check, so guesses at the catalog,
		// at progress sync and at the API all count against the same address.
		limiter := kosync.NewLimiter()
		srv.Limiter = limiter

		// Every route shares one listener so the reader needs a single
		// address for progress sync and for browsing. /opds goes to the
		// catalog and everything else to kosync, which owns the root help
		// page.
		root := http.NewServeMux()

		// The healthcheck means "this process is serving", nothing more: no
		// database query, no lock, no auth (spec §2). A broken library index
		// is not something a restart would fix.
		root.HandleFunc("GET /up", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "ok\n")
		})
		// Outpost's gateway owns /.outpost/ and never forwards it. Answering
		// it explicitly keeps kosync's catch-all from serving its help page
		// there to anything that reaches the port directly.
		root.Handle("/.outpost/", http.NotFoundHandler())

		// The index backs the catalog, the API and MCP alike, so it is open
		// even with the catalog off.
		db, err := a.index()
		if err != nil {
			return err
		}

		// Who a request is, for the catalog, the API and MCP. Under the
		// gateway a verified identity token; otherwise, or for a client
		// that cannot sign in there (the reader), a shelf account over
		// HTTP Basic. One account works everywhere, except one the reader
		// registered itself (see below).
		identify := gatewayIdentity(env, logger)
		authenticate := func(r *http.Request) (string, bool) {
			if identify != nil {
				if user, ok := identify(r); ok {
					return user, true
				}
			}
			user, password, ok := r.BasicAuth()
			if !ok || !limiter.Allowed(r) {
				return "", false
			}
			if store.CheckPassword(user, password) != nil {
				limiter.Failed(r)
				return "", false
			}
			limiter.Succeeded(r)
			// An account made through POST /users/create came through an
			// endpoint that asks nobody, open by default and reachable by
			// any process on the machine. It reads the catalog and syncs
			// progress, which is all registration ever offered; it does not
			// get the API, which uploads, rewrites books, adds devices and
			// resets other accounts' passwords.
			if self, err := store.SelfRegistered(user); err != nil || self {
				logger.Info("self-registered account refused outside progress sync and the catalog",
					"username", user, "remote", kosync.ClientIP(r))
				return "", false
			}
			return user, true
		}

		var catalogPath string
		if !*noOPDS && !cfg.OPDS.Disabled {
			catalog := opds.NewCatalog(db, catalogTitle(cfg))
			if cfg.OPDS.PageSize > 0 {
				catalog.PageSize = cfg.OPDS.PageSize
			}

			catalogSrv := opds.NewServer(catalog, logger)
			if !*openCatalog && !cfg.OPDS.Anonymous {
				catalogSrv.Auth = store
				catalogSrv.Identify = identify
				catalogSrv.Limiter = limiter
			}

			root.Handle("/opds", catalogSrv.Handler())
			root.Handle("/opds/", catalogSrv.Handler())
			catalogPath = "/opds"
		} else {
			// Say the catalog is off rather than letting kosync's catch-all
			// root handler answer /opds with its plain-text help page. An OPDS
			// client that asks for a feed and gets prose reports a parse
			// error, which points at the feed rather than at the switch that
			// turned it off.
			root.HandleFunc("/opds", catalogDisabled)
			root.HandleFunc("/opds/", catalogDisabled)
		}

		// AI models (spec §15), when Outpost hands shelf any. Optional in
		// every sense: unset variables, --no-ai, or a store that will not
		// open all leave shelf serving exactly as it would without them.
		var (
			aiClient   *ai.Client
			aiStore    *ai.Store
			classifier *ai.Classifier
		)
		if !*noAI {
			aiClient = ai.FromEnv()
		}
		if aiClient != nil {
			if aiStore, err = ai.OpenStore(cfg.Paths.AIFile()); err != nil {
				logger.Error("AI store unavailable; genres are off", "path", cfg.Paths.AIFile(), "err", err)
			} else {
				defer aiStore.Close()
				classifier = &ai.Classifier{Client: aiClient, Store: aiStore, DB: db, Log: logger}
			}
		}

		// REST under /v1, its OpenAPI document, and MCP at /mcp (spec §9).
		// Always authenticated, --open-catalog or not: unlike the catalog,
		// the API can edit shelves.
		svc := &api.Service{
			DB:             db,
			LibraryRoot:    cfg.LibraryRoot,
			Progress:       store,
			DefaultUser:    cfg.Kosync.User,
			SaveShelves:    a.saveShelves,
			ReloadShelves:  func() error { return db.LoadShelves(cfg.Paths.ShelvesFile()) },
			Version:        readVersion().String(),
			ImportTemplate: cfg.NamingTemplate,
			// Uploads stage beside the library's other scratch files, in
			// the cache: on the same volume as Outpost storage, so placing
			// one into the library is a rename rather than a second copy.
			StageDir: filepath.Join(cfg.Paths.Cache, "uploads"),
			// Reader accounts: what the e-reader signs in with.
			AccountDB: store,
		}
		if aiStore != nil {
			svc.Genres = aiStore
		}
		apiSrv := api.NewServer(svc, authenticate, logger)
		apiSrv.Realm = catalogTitle(cfg)
		apiSrv.Info = api.Info{Auth: "basic"}
		if identify != nil {
			// Sign-out ends the whole Outpost login; the gateway owns it.
			apiSrv.Info = api.Info{Auth: "gateway", LogoutURL: "/.outpost/logout"}
		}
		apiSrv.Mount(root)

		// Mail (spec §16): newsletters sent to shelf's addresses become books.
		var poller *mailbox.Poller
		if !*noMail {
			if mc := mailbox.FromEnv(); mc != nil {
				poller = &mailbox.Poller{
					Client:      mc,
					DB:          db,
					LibraryRoot: cfg.LibraryRoot,
					StateFile:   cfg.Paths.MailStateFile(),
					StageDir:    cfg.Paths.MailStageDir(),
					Log:         logger,
				}
				if aiClient != nil {
					poller.Summarize = summarizer(aiClient, logger)
				}
			}
		}

		// kosync owns only its own paths; the web UI has everything else.
		progressSync := srv.Handler()
		root.Handle("/users/", progressSync)
		root.Handle("/syncs/", progressSync)

		// The web UI is a client of /v1 like any other. Its page needs a
		// login too: a browser prompted here, on the first page, keeps the
		// credentials for every API call the page then makes, where a 401
		// from a script would leave it with nothing to show. Under the
		// gateway the browser arrives already signed in.
		webUI := ui.Handler(logger)
		root.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := authenticate(r); !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="`+strings.ReplaceAll(catalogTitle(cfg), `"`, "")+`", charset="UTF-8"`)
				http.Error(w, "sign in with your shelf account", http.StatusUnauthorized)
				return
			}
			webUI.ServeHTTP(w, r)
		}))

		httpSrv := &http.Server{
			Addr:              listen,
			Handler:           outpost.SameOrigin(root),
			ReadHeaderTimeout: 10 * time.Second,
			// The device is on Wi-Fi and can be slow; these are generous but
			// still bounded, so a stalled connection cannot hold a slot open
			// indefinitely.
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
			ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		}

		ln, err := net.Listen("tcp", listen)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", listen, err)
		}

		if env.Supervised() {
			logger.Info("listening",
				"addr", ln.Addr().String(),
				"progress_store", store.Path(),
				"library", cfg.LibraryRoot,
				"catalog", catalogPath != "",
				"registration", srv.AllowRegistration,
				"gateway", env.Gateway,
				"ai", classifier != nil,
				"mail", poller != nil)
		} else {
			printServerBanner(ln.Addr(), store.Path(), srv.AllowRegistration, catalogPath)
		}

		// Background work — the scan, AI classification, mail import — stops
		// with bgCtx and is waited for before the stores and index close, so
		// a SIGTERM never closes a database under a write in progress.
		var bg sync.WaitGroup
		defer bg.Wait()
		bgCtx, cancelBG := context.WithCancel(ctx)
		defer cancelBG()

		// Syncs started from the web UI or MCP run as jobs on bgCtx, and are
		// waited for (deferred after the stores, so run before they close).
		// Installed before Serve, so no request can see it half set up.
		configFile := a.configPath
		if configFile == "" {
			configFile = cfg.Paths.ConfigFile()
		}
		svc.Sync = api.NewSyncService(bgCtx, cfg, configFile)
		defer svc.Sync.Wait()
		apiSrv.Info.Features = api.Features{
			AI: classifier != nil, Mail: poller != nil, OPDS: catalogPath != "",
		}

		// enrich asks the classifier for a pass. Buffered and never blocking:
		// a request while one is pending is the same request.
		enrich := make(chan struct{}, 1)
		kick := func() {
			select {
			case enrich <- struct{}{}:
			default:
			}
		}
		if classifier != nil {
			bg.Go(func() { runClassifier(bgCtx, logger, classifier, enrich) })
		}
		if *scan {
			bg.Go(func() {
				scanLibrary(bgCtx, logger, db, cfg)
				kick()
			})
		} else {
			kick()
		}
		svc.OnImport = kick
		if poller != nil {
			poller.OnImport = kick
			bg.Go(func() { poller.Run(bgCtx) })
		}

		errCh := make(chan error, 1)
		go func() {
			if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()

		select {
		case err := <-errCh:
			// Cancel before the deferred waits run: svc.Sync.Wait is deferred
			// after cancelBG, so it runs first and would otherwise sit out a
			// sync job of many minutes before the process could exit.
			cancelBG()
			return err
		case <-ctx.Done():
			if env.Supervised() {
				logger.Info("shutting down")
			} else {
				fmt.Fprintln(os.Stderr, "\nshutting down…")
			}
			cancelBG()
			// Give in-flight requests a moment. A progress write that is
			// half-committed when the process exits is a reading position
			// silently lost, and this store is the only copy. Held under the
			// ten seconds Outpost allows before SIGKILL (spec §6), with room
			// left for the scan to notice cancellation and the stores to close.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
			defer cancel()
			if err := httpSrv.Shutdown(shutdownCtx); err != nil {
				logger.Warn("shutdown incomplete", "err", err)
			}
			return nil
		}
	},
}

// shutdownGrace is how long in-flight requests get after SIGTERM.
const shutdownGrace = 8 * time.Second

// listenAddr decides where to listen.
//
// --port and OUTPOST_PORT bind loopback only. They exist for running behind
// Outpost, where nothing should reach shelf except through its own node and
// gateway (spec §1); a reader on the LAN needs --listen or the config's
// kosync.listen instead. Under Outpost there is no fallback to the config's
// address: its default binds every interface, and a port nobody declared is
// one Outpost health-checks without anything listening.
func listenAddr(listen, legacy string, port int, env outpost.Env, configured string) (string, error) {
	if port < 0 || port > 65535 {
		return "", fmt.Errorf("%w: --port %d is out of range", errUsage, port)
	}
	switch {
	case listen != "":
		return listen, nil
	case legacy != "":
		return legacy, nil
	case port > 0:
		return loopback(port), nil
	case env.Port > 0:
		return loopback(env.Port), nil
	case env.Supervised():
		return "", fmt.Errorf("no port: pass --port or set %s", outpost.EnvPort)
	case configured != "":
		return configured, nil
	default:
		return ":8080", nil
	}
}

func loopback(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

// serveLogger picks the log format.
//
// Under Outpost, one JSON object per line with no timestamp: Outpost stamps
// every line itself and reads the level and message from these keys (spec §7).
// Run by hand, the human-readable text format on stderr, as before.
func serveLogger(env outpost.Env, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	if env.Supervised() {
		return slog.New(outpostLogHandler(os.Stdout, level))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// outpostLogHandler writes the JSON lines Outpost parses, minus the time.
func outpostLogHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
}

// gatewayIdentity recognises requests Outpost's login gateway has signed in.
//
// Returns nil — Basic only — unless the gateway is on and has said where its
// keys are. Only the signed X-Outpost-Identity is accepted, never the plain
// X-Outpost-User beside it: the catalog serves the whole library, and any
// process on the machine can reach the loopback port and set that header.
func gatewayIdentity(env outpost.Env, logger *slog.Logger) func(*http.Request) (string, bool) {
	if !env.Gateway {
		return nil
	}
	v := outpost.NewVerifier(env)
	if v == nil {
		logger.Warn("login gateway is on but OUTPOST_ISSUER or OUTPOST_JWKS_URL is unset; " +
			"the catalog will ask signed-in users for their shelf password")
		return nil
	}
	return func(r *http.Request) (string, bool) {
		id, err := v.VerifyRequest(r)
		if err != nil {
			if !errors.Is(err, outpost.ErrNoToken) {
				logger.Warn("identity token rejected", "err", err, "path", r.URL.Path)
			}
			return "", false
		}
		return id.User, true
	}
}

// classifyInterval is how often the classifier looks for books added by
// some route it was not told about, such as `shelf import` run by hand.
const classifyInterval = time.Hour

// runClassifier runs a classification pass when asked and once an hour, one
// pass at a time. A pass costs nothing for books already classified, so the
// hourly one only ever spends on new books.
func runClassifier(ctx context.Context, logger *slog.Logger, c *ai.Classifier, enrich <-chan struct{}) {
	tick := time.NewTicker(classifyInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-enrich:
		case <-tick.C:
		}
		if err := c.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("genre classification failed", "err", err)
		}
	}
}

// summarizer has the operator's LLM write a mail-derived book's description.
//
// Asked once per message, when it is imported, and baked into the EPUB, so
// it is never asked again. Errors are returned for the poller to ignore:
// the message is imported without a summary rather than held back.
func summarizer(c *ai.Client, logger *slog.Logger) func(ctx context.Context, subject, text string) (string, error) {
	const system = "You write the description for an email newsletter that has been " +
		"turned into an e-book. Reply with two or three plain sentences saying what " +
		"it covers. No preamble, no markdown, no quotation marks."
	return func(ctx context.Context, subject, text string) (string, error) {
		out, model, err := c.Chat(ctx, system, "Subject: "+subject+"\n\n"+text, 200)
		if err != nil {
			logger.Debug("mail summary unavailable", "err", err)
			return "", err
		}
		logger.Debug("mail summarised", "model", model)
		return strings.TrimSpace(out), nil
	}
}

// scanLibrary brings the index up to date with the library on disk.
//
// Run in the background because a large first scan takes minutes and the
// healthcheck must not wait for it: the index is a cache, so serving it while
// it fills is correct, only incomplete.
func scanLibrary(ctx context.Context, logger *slog.Logger, db *library.DB, cfg *config.Config) {
	// The default library under Outpost storage does not exist on a fresh
	// install; creating it gives the operator somewhere obvious to put books.
	if dir := cfg.Paths.LibraryDir(); dir != "" && dir == cfg.LibraryRoot {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			logger.Error("create library directory", "path", dir, "err", err)
			return
		}
	}

	logger.Info("scan started", "library", cfg.LibraryRoot)
	res, err := db.Scan(ctx, cfg.LibraryRoot, library.ScanOptions{
		Progress: func(p library.ScanProgress) {
			if p.Action == library.ActionFailed {
				logger.Warn("could not index", "path", p.Path, "err", p.Err)
			}
		},
	})
	switch {
	case errors.Is(err, context.Canceled):
		logger.Info("scan interrupted")
	case err != nil:
		// Not fatal: the previous index still serves, and exiting would have
		// Outpost restart a process whose real problem is a missing directory.
		logger.Error("scan failed", "library", cfg.LibraryRoot, "err", err)
	default:
		logger.Info("scan finished",
			"added", res.Added, "updated", res.Updated, "unchanged", res.Unchanged,
			"removed", res.Removed, "skipped", res.Skipped, "failed", len(res.Errors))
	}
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

	fmt.Printf("\nWeb UI: http://localhost:%s/ (an account from `shelf user add` signs in)\n", port)
	fmt.Println("API: /v1 (described at /v1/openapi.json); MCP: /mcp. Both take the same account.")
	fmt.Println("An account the reader registers itself is for progress sync and the catalog only.")
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
