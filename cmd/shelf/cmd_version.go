package main

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

var cmdVersion = &command{
	name:    "version",
	summary: "print the version and how this binary was built",
	usage:   "version [--full]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("version")
		full := fs.Bool("full", false, "include build settings and dependency versions")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		v := readVersion()
		fmt.Printf("shelf %s\n", v.String())
		if !*full {
			return nil
		}

		fmt.Printf("  go        %s\n", runtime.Version())
		fmt.Printf("  platform  %s/%s\n", runtime.GOOS, runtime.GOARCH)
		if v.Revision != "" {
			fmt.Printf("  revision  %s\n", v.Revision)
		}
		if v.Time != "" {
			fmt.Printf("  built     %s\n", v.Time)
		}
		for _, dep := range v.Deps {
			fmt.Printf("  dep       %s\n", dep)
		}
		return nil
	},
}

// Version describes what this binary is.
//
// Read from the build info the toolchain stamps in rather than from a variable
// set with -ldflags. A plain `go build` records the VCS revision and whether
// the tree was dirty, and `go install module@v1.2.3` records the module
// version, so both routes produce something truthful with no build wrapper to
// remember. The one case with nothing to report is a `go test` binary, whose
// build info carries neither — which is also why decantPinnedVersion exists.
type Version struct {
	Module   string
	Revision string
	Time     string
	Dirty    bool
	Deps     []string
}

// String renders the version the way it should appear in a bug report.
func (v Version) String() string {
	switch {
	case v.Module != "" && v.Module != "(devel)":
		return v.Module
	case v.Revision != "":
		short := v.Revision
		if len(short) > 12 {
			short = short[:12]
		}
		if v.Dirty {
			// Worth saying out loud. A dirty build is not reproducible from
			// the revision alone, and a bug report quoting only the hash sends
			// someone looking at code that was never what ran.
			return short + " (modified working tree)"
		}
		return short
	default:
		return "devel (no build information)"
	}
}

func readVersion() Version {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version{}
	}

	v := Version{Module: info.Main.Version}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Revision = s.Value
		case "vcs.time":
			v.Time = s.Value
		case "vcs.modified":
			v.Dirty = s.Value == "true"
		}
	}

	for _, d := range info.Deps {
		// Only the dependencies whose version changes shelf's output. The full
		// module graph is noise in a bug report, and `go version -m` prints it
		// for anyone who wants it.
		if strings.Contains(d.Path, "decant") || strings.Contains(d.Path, "sqlite") {
			v.Deps = append(v.Deps, d.Path+" "+d.Version)
		}
	}
	return v
}
