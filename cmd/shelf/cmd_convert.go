package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sroberts/shelf/internal/convert"
)

var cmdConvert = &command{
	name:    "convert",
	summary: "convert PDF, TXT, and other sources to EPUB",
	usage:   "convert FILE... [--converter NAME] [--out DIR] [--force] [--json]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("convert")
		converterName := fs.String("converter", "", "converter preset (default: from config)")
		outDir := fs.String("out", "", "write EPUBs here (default: alongside each source)")
		force := fs.Bool("force", false, "reconvert even if a cached result exists")
		cacheOnly := fs.Bool("cache-only", false, "populate the cache without writing an output file")
		asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
		timeout := fs.Duration("timeout", 0, "per-file timeout (default: from config)")
		listPresets := fs.Bool("list", false, "list available converters and exit")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		if *listPresets {
			return listConverters()
		}
		if fs.NArg() == 0 {
			return fmt.Errorf("%w: no files given", errUsage)
		}

		cfg, err := a.config()
		if err != nil {
			return err
		}

		limit := cfg.Convert.Timeout.Duration
		if *timeout > 0 {
			limit = *timeout
		}

		// An explicit --converter is honored strictly: the user named it, so
		// silently running something else would be wrong. The configured
		// default answers "what converts my PDFs" and is allowed to fall back
		// per format, since the built-in default reads PDF only.
		var resolve func(string) (*convert.Converter, error)
		if *converterName != "" {
			conv, err := convert.New(*converterName, limit)
			if err != nil {
				// A missing binary is a setup problem; the error already says
				// how to fix it, so do not bury it under a stack of context.
				return err
			}
			resolve = func(string) (*convert.Converter, error) { return conv, nil }
		} else {
			resolve = func(src string) (*convert.Converter, error) {
				return convert.NewForFile(cfg.Convert.PDF, src, limit)
			}
		}

		cache := convert.NewCache(filepath.Join(cfg.Paths.Cache, "converted"))

		var failed int
		for _, src := range fs.Args() {
			if err := ctx.Err(); err != nil {
				return err
			}
			conv, err := resolve(src)
			if err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(src), err)
				continue
			}
			if err := convertOne(ctx, conv, cache, src, *outDir, *force, *cacheOnly, *asJSON); err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(src), err)
			}
		}

		if failed > 0 {
			return fmt.Errorf("%d of %d file(s) failed to convert", failed, fs.NArg())
		}
		return nil
	},
}

func convertOne(ctx context.Context, conv *convert.Converter, cache *convert.Cache,
	src, outDir string, force, cacheOnly, asJSON bool) error {

	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}

	opts := convert.Options{}
	if !asJSON {
		opts.Progress = func(msg string) { fmt.Fprintf(os.Stderr, "  %s\n", msg) }
	}

	if force {
		// Bypassing the cache means converting straight to the destination.
		dst, derr := destination(src, outDir)
		if derr != nil {
			return derr
		}
		res, cerr := conv.Convert(ctx, src, dst, opts)
		if cerr != nil {
			return cerr
		}
		return report(src, dst, res.Assessment, res.Duration, res.OutputSize, false, asJSON)
	}

	artifact, entry, cached, err := cache.Convert(ctx, conv, src, opts)
	if err != nil {
		return err
	}

	assessment := convert.Assessment{}
	var size int64
	if entry != nil {
		assessment = entry.Assessment
		size = entry.OutputSize
	}

	dst := artifact
	if !cacheOnly {
		if dst, err = destination(src, outDir); err != nil {
			return err
		}
		if err := copyFile(artifact, dst); err != nil {
			return err
		}
	}
	return report(src, dst, assessment, 0, size, cached, asJSON)
}

// destination computes where the converted EPUB should land.
func destination(src, outDir string) (string, error) {
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)) + ".epub"
	if outDir == "" {
		return filepath.Join(filepath.Dir(src), base), nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(outDir, base), nil
}

// report prints the outcome, leading with the quality verdict when it is not
// good. Being honest about a bad conversion before it reaches the device is
// the whole point of assessing it.
func report(src, dst string, a convert.Assessment, dur time.Duration, size int64, cached bool, asJSON bool) error {
	if asJSON {
		return emitJSON(struct {
			Source     string   `json:"source"`
			Output     string   `json:"output"`
			Cached     bool     `json:"cached"`
			Size       int64    `json:"size"`
			Quality    string   `json:"quality"`
			Reasons    []string `json:"reasons,omitempty"`
			TextChars  int      `json:"text_chars"`
			Images     int      `json:"images"`
			DurationMS int64    `json:"duration_ms,omitempty"`
		}{
			Source: src, Output: dst, Cached: cached, Size: size,
			Quality: string(a.Quality), Reasons: a.Reasons,
			TextChars: a.TextChars, Images: a.Images,
			DurationMS: dur.Milliseconds(),
		})
	}

	note := ""
	switch {
	case cached:
		note = " (cached)"
	case dur > 0:
		note = fmt.Sprintf(" (%s)", dur.Round(time.Second))
	}
	fmt.Printf("%s -> %s  %s%s\n", filepath.Base(src), dst, humanSize(size), note)

	switch a.Quality {
	case convert.QualityGood:
		// Nothing to say; silence is the good outcome.
	case convert.QualityUnknown:
		fmt.Fprintf(os.Stderr, "  note: could not assess the output\n")
	default:
		fmt.Fprintf(os.Stderr, "  %s: %s\n", strings.ToUpper(string(a.Quality)),
			strings.Join(a.Reasons, "; "))
	}
	return nil
}

func listConverters() error {
	// Every converter is compiled in, so there is no availability to report and
	// nothing to install. The version matters instead: it is part of the cache
	// key, so an upgrade re-converts rather than serving older output.
	for _, name := range convert.PresetNames() {
		p := convert.Presets[name]

		version := p.Version
		if version == "" {
			version = "built-in"
		}
		fmt.Printf("%-10s %-10s %s\n", name, version, p.Notes)
		fmt.Printf("%-10s %-10s %s\n", "", "", "formats: "+strings.Join(p.Formats, ", "))
	}
	return nil
}

// copyFile writes src to dst atomically.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// os.CreateTemp makes a 0600 file. Carrying the source's mode across keeps
	// a derived artifact no less readable than the book it came from, which
	// matters because these land in the user's library.
	mode := os.FileMode(0o644)
	if fi, err := in.Stat(); err == nil {
		mode = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".shelf-out-*")
	if err != nil {
		return err
	}
	name := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
