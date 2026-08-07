package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/sroberts/shelf/internal/kosync"
)

var cmdUser = &command{
	name:    "user",
	summary: "manage reading-progress sync accounts",
	usage:   "user add|ls|rm|passwd [NAME]",
	run: func(ctx context.Context, a *app, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("%w: expected a subcommand (add, ls, rm, passwd)", errUsage)
		}
		switch args[0] {
		case "add", "create":
			return userAdd(a, args[1:])
		case "ls", "list":
			return userList(a, args[1:])
		case "rm", "delete":
			return userRemove(a, args[1:])
		case "passwd", "password":
			return userPasswd(a, args[1:])
		default:
			return fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
		}
	},
}

// openProgressStore opens the sync store.
//
// Opened directly rather than over HTTP, so accounts can be managed whether or
// not `shelf serve` is running. SQLite in WAL mode handles the concurrent
// access, and the store carries a busy timeout for the case where the server is
// mid-write.
func openProgressStore(a *app) (*kosync.Store, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	if err := cfg.Paths.EnsureDirs(); err != nil {
		return nil, err
	}
	return kosync.OpenStore(cfg.Paths.ProgressFile())
}

func userAdd(a *app, args []string) error {
	fs := newFlagSet("user add")
	password := fs.String("password", "",
		"password (prompted for if omitted; a flag is visible in ps and shell history)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: expected exactly one username", errUsage)
	}
	username := strings.TrimSpace(fs.Arg(0))

	store, err := openProgressStore(a)
	if err != nil {
		return err
	}
	defer store.Close()

	pw := *password
	if pw == "" {
		// Confirmed twice on purpose. A mistyped password here surfaces on the
		// reader as a bare "Authentication failed", which says nothing about
		// where the fault is and is genuinely hard to work back from.
		if pw, err = promptNewPassword(); err != nil {
			return err
		}
	}
	if pw == "" {
		return errors.New("password must not be empty")
	}

	if err := store.CreateUser(username, kosync.PasswordKey(pw)); err != nil {
		if errors.Is(err, kosync.ErrUserExists) {
			return fmt.Errorf("%q already exists; use 'shelf user passwd %s' to change its password",
				username, username)
		}
		return err
	}

	fmt.Printf("created %q\n\n", username)
	printReaderInstructions(a, username)
	return nil
}

func userList(a *app, args []string) error {
	fs := newFlagSet("user ls")
	asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	store, err := openProgressStore(a)
	if err != nil {
		return err
	}
	defer store.Close()

	users, err := store.UserList()
	if err != nil {
		return err
	}

	if *asJSON {
		for _, u := range users {
			if err := emitJSON(struct {
				Username string `json:"username"`
				Created  int64  `json:"created_unix"`
				Books    int    `json:"books_with_progress"`
			}{u.Username, u.CreatedAt, u.Books}); err != nil {
				return err
			}
		}
		return nil
	}

	if len(users) == 0 {
		fmt.Fprintln(os.Stderr,
			"no sync accounts yet — create one with 'shelf user add <name>'")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USERNAME\tBOOKS READ\tCREATED")
	for _, u := range users {
		fmt.Fprintf(w, "%s\t%d\t%s\n", u.Username, u.Books,
			time.Unix(u.CreatedAt, 0).Format("2006-01-02"))
	}
	return w.Flush()
}

func userRemove(a *app, args []string) error {
	fs := newFlagSet("user rm")
	yes := fs.Bool("yes", false, "do not prompt for confirmation")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: expected exactly one username", errUsage)
	}
	username := strings.TrimSpace(fs.Arg(0))

	store, err := openProgressStore(a)
	if err != nil {
		return err
	}
	defer store.Close()

	// Say what will be lost before asking. Reading positions live only here;
	// the device pushes them and keeps no copy of its own.
	books := 0
	if users, err := store.UserList(); err == nil {
		for _, u := range users {
			if u.Username == username {
				books = u.Books
			}
		}
	}

	if !*yes {
		fmt.Fprintf(os.Stderr, "Deleting %q also deletes reading positions for %d book(s).\n",
			username, books)
		fmt.Fprintln(os.Stderr, "Those exist nowhere else — the reader does not keep a copy.")
		if !confirm(fmt.Sprintf("Delete %q?", username)) {
			return errors.New("cancelled")
		}
	}

	if err := store.DeleteUser(username); err != nil {
		return err
	}
	fmt.Printf("deleted %q\n", username)
	return nil
}

func userPasswd(a *app, args []string) error {
	fs := newFlagSet("user passwd")
	password := fs.String("password", "", "new password (prompted for if omitted)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: expected exactly one username", errUsage)
	}
	username := strings.TrimSpace(fs.Arg(0))

	store, err := openProgressStore(a)
	if err != nil {
		return err
	}
	defer store.Close()

	pw := *password
	if pw == "" {
		if pw, err = promptNewPassword(); err != nil {
			return err
		}
	}
	if pw == "" {
		return errors.New("password must not be empty")
	}

	if err := store.SetPassword(username, kosync.PasswordKey(pw)); err != nil {
		return err
	}
	fmt.Printf("password changed for %q\n\n", username)
	printReaderInstructions(a, username)
	return nil
}

// printReaderInstructions says what to type into the device.
//
// The address matters as much as the credentials: the reader needs one it can
// route to, and it must be http rather than https — shelf's server is plain
// HTTP, and a TLS handshake against it fails before any credentials are read,
// which the device reports as an authentication failure.
func printReaderInstructions(a *app, username string) {
	cfg, err := a.config()
	port := "8080"
	if err == nil && cfg.Kosync.Listen != "" {
		if _, p, ok := strings.Cut(cfg.Kosync.Listen, ":"); ok && p != "" {
			port = p
		}
	}

	fmt.Println("In the reader's KOReader sync settings:")
	hosts := lanAddresses()
	if len(hosts) == 0 {
		fmt.Printf("    server:   http://<this-machine>:%s\n", port)
	} else {
		for i, h := range hosts {
			label := "server:  "
			if i > 0 {
				label = "      or "
			}
			fmt.Printf("    %s http://%s:%s\n", label, h, port)
		}
	}
	fmt.Printf("    username: %s\n", username)
	fmt.Println("    password: (what you just entered)")
	fmt.Println()
	fmt.Println("Use http, not https. Log in rather than register — the account already exists.")
	fmt.Println("The server must be running: shelf serve")
}

// promptNewPassword reads a password twice without echoing it.
func promptNewPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Reading a password from a pipe without echo control would silently
		// leave it on screen in some setups; require the flag instead.
		return "", errors.New("not a terminal; pass --password")
	}

	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}

	fmt.Fprint(os.Stderr, "Again: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}

	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}

// confirm asks a yes/no question, defaulting to no.
func confirm(question string) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		fmt.Fprintln(os.Stderr, "refusing to prompt non-interactively; pass --yes")
		return false
	}

	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
