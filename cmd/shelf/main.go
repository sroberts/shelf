// Command shelf manages an e-book library and syncs it to a CrossPoint reader.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
)

// Exit codes. Distinguishing usage errors from runtime failures is what makes
// the tool scriptable.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// errUsage marks an error as a usage problem rather than a runtime failure.
var errUsage = errors.New("usage")

type command struct {
	name    string
	summary string
	usage   string
	run     func(ctx context.Context, app *app, args []string) error
}

func commands() []*command {
	return []*command{
		cmdScan,
		cmdLs,
		cmdMeta,
		cmdShelf,
		cmdImport,
		cmdConvert,
		cmdOptimize,
		cmdTags,
		cmdDevices,
		cmdSync,
		cmdPush,
		cmdPull,
		cmdServe,
		cmdDoctor,
	}
}

func main() {
	os.Exit(run())
}

func run() int {
	// A global flag set so `shelf --help` and `shelf --config=...` work before
	// a subcommand is chosen.
	var (
		configPath = flag.String("config", "", "path to config.toml (default: XDG config directory)")
		libraryDir = flag.String("library", "", "library root (overrides config)")
		noTUI      = flag.Bool("no-tui", false, "never launch the terminal interface")
		showHelp   = flag.Bool("help", false, "show help")
	)
	flag.Usage = func() { usage(os.Stderr) }
	flag.Parse()

	args := flag.Args()
	if *showHelp {
		usage(os.Stdout)
		return exitOK
	}

	// Ctrl-C cancels the context. Long operations check it and stop cleanly
	// rather than being killed mid-write.
	rootCtx, stopSignals := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// Bare `shelf` opens the TUI. With --no-tui, or when stdout is not a
	// terminal, fall back to help so a script does not get escape-code soup.
	if len(args) == 0 {
		if *noTUI || !isInteractive() {
			usage(os.Stdout)
			return exitUsage
		}

		app := &app{configPath: *configPath, libraryOverride: *libraryDir}
		defer app.close()

		if err := runTUI(rootCtx, app); err != nil {
			fmt.Fprintf(os.Stderr, "shelf: %v\n", err)
			return exitError
		}
		return exitOK
	}

	name := args[0]
	cmd := lookup(name)
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "shelf: unknown command %q\n", name)
		if suggestion := closest(name); suggestion != "" {
			fmt.Fprintf(os.Stderr, "did you mean %q?\n", suggestion)
		}
		fmt.Fprintf(os.Stderr, "run 'shelf --help' for the command list\n")
		return exitUsage
	}

	app := &app{configPath: *configPath, libraryOverride: *libraryDir}
	defer app.close()

	err := cmd.run(rootCtx, app, args[1:])
	switch {
	case err == nil, errors.Is(err, errHelpShown):
		return exitOK

	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, "shelf: interrupted")
		return exitError

	case errors.Is(err, errUsage):
		fmt.Fprintf(os.Stderr, "shelf %s: %v\n\n", cmd.name, err)
		fmt.Fprintf(os.Stderr, "usage: shelf %s\n", cmd.usage)
		return exitUsage

	default:
		fmt.Fprintf(os.Stderr, "shelf %s: %v\n", cmd.name, err)
		return exitError
	}
}

func lookup(name string) *command {
	for _, c := range commands() {
		if c.name == name {
			return c
		}
	}
	return nil
}

// closest suggests a command for a near miss, using a cheap prefix and
// substring check rather than a full edit distance.
func closest(name string) string {
	name = strings.ToLower(name)
	for _, c := range commands() {
		if strings.HasPrefix(c.name, name) || strings.HasPrefix(name, c.name) {
			return c.name
		}
	}
	return ""
}

func usage(w *os.File) {
	fmt.Fprint(w, `shelf - a library manager for CrossPoint e-readers

usage: shelf [global flags] <command> [flags] [arguments]

Commands:
`)
	cmds := commands()
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].name < cmds[j].name })

	width := 0
	for _, c := range cmds {
		if len(c.name) > width {
			width = len(c.name)
		}
	}
	for _, c := range cmds {
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.name, c.summary)
	}

	fmt.Fprint(w, `
Global flags:
  --config PATH    path to config.toml
  --library PATH   library root, overriding the config file
  --no-tui         never launch the terminal interface
  --help           show this help

Run 'shelf' with no command to open the terminal interface.
Run 'shelf <command> --help' for command-specific flags.
`)
}

// newFlagSet builds a flag set that reports usage errors through errUsage.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// parseFlags parses args, converting flag errors into usage errors.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(permuteArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// The flag package already printed the help text.
			return errHelpShown
		}
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	return nil
}

// boolFlag matches flag values that take no argument.
type boolFlag interface{ IsBoolFlag() bool }

// permuteArgs moves flags ahead of positional arguments.
//
// The standard flag package stops parsing at the first non-flag argument, so
// `shelf ls earthsea --json` would silently ignore --json and fold it into the
// query. Everyone expects trailing flags to work, and silently ignoring one is
// worse than rejecting it, so the argument list is reordered before parsing.
// A literal "--" ends flag parsing, as usual.
func permuteArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]

		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}

		flags = append(flags, a)
		// "-flag=value" carries its own value.
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if hasValue {
			continue
		}
		// A non-boolean flag consumes the next argument as its value.
		f := fs.Lookup(name)
		if f == nil {
			continue // unknown; let flag.Parse produce the error
		}
		if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

// errHelpShown signals that --help was handled and the process should exit
// cleanly without printing anything further.
var errHelpShown = errors.New("help shown")
