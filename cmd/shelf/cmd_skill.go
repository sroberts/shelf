package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// skillDoc is the agent skill that ships inside the binary, so the version an
// agent installs always describes the commands this binary actually has.
//
//go:embed skill/SKILL.md
var skillDoc []byte

var cmdSkill = &command{
	name:    "skill",
	summary: "print or install the agent skill that teaches an LLM to drive shelf",
	usage:   "skill show | skill install [--dir DIR] [--force]",
	run: func(ctx context.Context, a *app, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("%w: expected a subcommand (show, install)", errUsage)
		}
		switch args[0] {
		case "show", "print":
			_, err := os.Stdout.Write(skillDoc)
			return err
		case "install":
			return skillInstall(args[1:])
		default:
			return fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
		}
	},
}

func skillInstall(args []string) error {
	flags := newFlagSet("skill install")
	dir := flags.String("dir", "", "skills directory (default: ~/.claude/skills)")
	force := flags.Bool("force", false, "replace an existing SKILL.md that differs")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("%w: unexpected argument %q", errUsage, flags.Arg(0))
	}

	root := *dir
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("find home directory: %w (pass --dir)", err)
		}
		root = filepath.Join(home, ".claude", "skills")
	}
	dest := filepath.Join(root, "shelf", "SKILL.md")

	existing, err := os.ReadFile(dest)
	switch {
	case err == nil && bytes.Equal(existing, skillDoc):
		fmt.Printf("%s is already up to date\n", dest)
		return nil
	case err == nil && !*force:
		// Someone may have tailored it; replacing their edits silently is
		// the one thing an installer must not do.
		return fmt.Errorf("%s exists and differs from this version; pass --force to replace it", dest)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, skillDoc, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	fmt.Printf("installed %s\n", dest)
	return nil
}
