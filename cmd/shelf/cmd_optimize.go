package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sroberts/shelf/internal/convert"
	"github.com/sroberts/shelf/internal/epub"
)

var cmdOptimize = &command{
	name:    "optimize",
	summary: "rebuild an EPUB targeted at a device's panel",
	usage:   "optimize FILE... --profile NAME [--out DIR] [--in-place] [--dither] [--force] [--json]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("optimize")
		profileName := fs.String("profile", "", "device profile (e.g. x4)")
		outDir := fs.String("out", "", "write optimized copies here")
		inPlace := fs.Bool("in-place", false, "overwrite each source file")
		dither := fs.Bool("dither", false, "apply error diffusion to images")
		force := fs.Bool("force", false, "re-optimize even if a cached result exists")
		asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
		listProfiles := fs.Bool("list", false, "list available profiles and exit")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		if *listProfiles {
			return listOptimizeProfiles()
		}
		if fs.NArg() == 0 {
			return fmt.Errorf("%w: no files given", errUsage)
		}
		if *inPlace && *outDir != "" {
			return fmt.Errorf("%w: --in-place and --out are mutually exclusive", errUsage)
		}
		if *profileName == "" {
			return fmt.Errorf("%w: --profile is required (see --list)", errUsage)
		}

		profile, err := convert.LookupProfile(*profileName)
		if err != nil {
			return err
		}
		if *dither {
			profile.Dither = true
		}

		cfg, err := a.config()
		if err != nil {
			return err
		}
		// Panel dimensions are not reported by the firmware, so a configured
		// value is the only way they can be known. Warn once rather than
		// letting the user believe images were resized when they were not.
		if !profile.KnowsPanelSize() && !*asJSON {
			fmt.Fprintf(os.Stderr,
				"note: no panel dimensions for profile %q; images will be converted to grayscale\n"+
					"      and recompressed but not downscaled\n", profile.Name)
		}

		cache := convert.NewOptimizeCache(cfg.Paths.OptimizedDir())

		var failed int
		for _, src := range fs.Args() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := optimizeOne(cache, profile, src, *outDir, *inPlace, *force, *asJSON); err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(src), err)
			}
		}

		if failed > 0 {
			return fmt.Errorf("%d of %d file(s) failed to optimize", failed, fs.NArg())
		}
		return nil
	},
}

func optimizeOne(cache *convert.OptimizeCache, p convert.Profile,
	src, outDir string, inPlace, force, asJSON bool) error {

	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	if !epub.IsEPUB(src) {
		// Optimization rewrites an EPUB's images; a PDF has to be converted
		// first. Say so plainly instead of failing inside the zip reader.
		return fmt.Errorf("not an EPUB (convert it first with `shelf convert`)")
	}

	// --force writes straight to the destination, bypassing the cache
	// entirely, so a user debugging a bad profile is never handed a stale
	// artifact.
	if force {
		dst, derr := optimizeDestination(src, outDir, inPlace)
		if derr != nil {
			return derr
		}
		res, oerr := convert.Optimize(src, dst, p)
		if oerr != nil {
			return oerr
		}
		return reportOptimize(res, false, asJSON)
	}

	artifact, entry, cached, err := cache.Optimize(src, p)
	if err != nil {
		return err
	}

	res := &convert.OptimizeResult{
		Source:  src,
		Output:  artifact,
		Profile: p.Name,
	}
	if entry != nil {
		res.SourceSize = entry.SourceSize
		res.OutputSize = entry.OutputSize
		res.Images = entry.Images
		res.Rewritten = entry.Rewritten
	}

	dst, err := optimizeDestination(src, outDir, inPlace)
	if err != nil {
		return err
	}
	if dst != "" {
		if err := copyFile(artifact, dst); err != nil {
			return err
		}
		res.Output = dst
	}
	return reportOptimize(res, cached, asJSON)
}

// optimizeDestination computes where the optimized copy should land. An empty
// return means the caller should leave the artifact in the cache.
func optimizeDestination(src, outDir string, inPlace bool) (string, error) {
	if inPlace {
		return src, nil
	}
	if outDir == "" {
		// Default to leaving the result in the cache: optimization is a
		// derived artifact, and the library keeps the original (spec.md 9.2).
		return "", nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(outDir, filepath.Base(src)), nil
}

func reportOptimize(r *convert.OptimizeResult, cached, asJSON bool) error {
	if asJSON {
		return emitJSON(struct {
			*convert.OptimizeResult
			Cached bool  `json:"cached"`
			Saved  int64 `json:"saved"`
		}{OptimizeResult: r, Cached: cached, Saved: r.Saved()})
	}

	note := ""
	if cached {
		note = " (cached)"
	}
	fmt.Printf("%s -> %s  %s%s\n", filepath.Base(r.Source), r.Output, humanSize(r.OutputSize), note)

	if saved := r.Saved(); saved > 0 {
		pct := float64(saved) / float64(r.SourceSize) * 100
		fmt.Printf("  %s smaller (%.0f%%), %d of %d image(s) rewritten\n",
			humanSize(saved), pct, r.Rewritten, r.Images)
	} else {
		fmt.Printf("  no saving; kept the original bytes\n")
	}

	for _, s := range r.Skipped {
		fmt.Fprintf(os.Stderr, "  skipped %s\n", s)
	}
	return nil
}

func listOptimizeProfiles() error {
	for _, name := range convert.ProfileNames() {
		p, err := convert.LookupProfile(name)
		if err != nil {
			continue
		}

		size := "panel size unknown"
		if p.KnowsPanelSize() {
			size = fmt.Sprintf("%dx%d", p.MaxWidth, p.MaxHeight)
		}

		var opts []string
		if p.Grayscale {
			opts = append(opts, "grayscale")
		}
		if p.Dither {
			opts = append(opts, "dither")
		}
		opts = append(opts, fmt.Sprintf("jpeg q%d", p.JPEGQuality))

		model := p.Model
		if model == "" {
			model = "any"
		}
		fmt.Printf("%-12s %-6s %-20s %s\n", name, model, size, strings.Join(opts, ", "))
	}
	return nil
}
