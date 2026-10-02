package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// deviceBlock is the subset of Device a frontend writes back. Empty fields are
// left out so the loader's defaults apply, exactly as if the user had typed the
// block by hand without them.
type deviceBlock struct {
	Nickname  string    `toml:"nickname"`
	Host      string    `toml:"host,omitempty"`
	Root      string    `toml:"root,omitempty"`
	Transport Transport `toml:"transport,omitempty"`
	Mount     string    `toml:"mount,omitempty"`
}

// AppendDevice adds a [[device]] block to the end of a config file and returns
// the configuration as it loads afterwards.
//
// The file is appended to, never re-encoded: config.toml is hand-written, and
// marshalling the parsed struct back out would discard every comment and the
// user's ordering. A new array-of-tables entry at the end of a TOML document is
// always valid wherever the previous table left off.
//
// The result is written beside the original and loaded through LoadFile before
// it replaces anything, so a block that fails validation (a duplicate
// nickname, say) leaves the existing file untouched and returns the same error
// a hand edit would have produced.
func AppendDevice(file string, paths Paths, d Device) (Config, error) {
	// Write through a symlink rather than over it: a config.toml managed by a
	// dotfiles repo is commonly a link, and renaming onto the link would
	// replace it with a regular file.
	target := file
	if resolved, err := filepath.EvalSymlinks(file); err == nil {
		target = resolved
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("resolve %s: %w", file, err)
	}

	existing, err := os.ReadFile(target)
	mode := fs.FileMode(0o600) // may hold sync credentials; private by default
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Config{}, fmt.Errorf("create config directory: %w", err)
		}
	case err != nil:
		return Config{}, fmt.Errorf("read %s: %w", target, err)
	default:
		if info, err := os.Stat(target); err == nil {
			mode = info.Mode().Perm()
		}
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 {
		if !bytes.HasSuffix(existing, []byte("\n")) {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("# Added by shelf.\n")
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	block := struct {
		Device []deviceBlock `toml:"device"`
	}{[]deviceBlock{{
		Nickname:  d.Nickname,
		Host:      d.Host,
		Root:      d.Root,
		Transport: d.Transport,
		Mount:     d.Mount,
	}}}
	if err := enc.Encode(block); err != nil {
		return Config{}, fmt.Errorf("encode device: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".config-*.toml")
	if err != nil {
		return Config{}, fmt.Errorf("stage config: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return Config{}, fmt.Errorf("stage config: %w", err)
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return Config{}, fmt.Errorf("stage config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Config{}, fmt.Errorf("stage config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Config{}, fmt.Errorf("stage config: %w", err)
	}

	cfg, err := LoadFile(tmpName, paths)
	if err != nil {
		return Config{}, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return Config{}, fmt.Errorf("save %s: %w", target, err)
	}
	committed = true

	cfg.File = file
	return cfg, nil
}
