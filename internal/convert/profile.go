package convert

import (
	"fmt"
	"sort"
	"strings"
)

// Device-targeted optimization profiles.
//
// A profile is the complete description of what optimizing for one device
// means. It is versioned in its name ("x4-v1") because the manifest records
// which profile produced an uploaded file: bumping the version is what makes
// shelf re-optimize and re-upload, and leaving it alone is what guarantees a
// stable profile produces no churn.
//
// Panel dimensions live here as data rather than in the optimizer's code, so
// supporting a new device is a table entry — see spec.md 9.2.

// Profile describes one device's optimization target.
type Profile struct {
	// Name identifies the profile and is recorded in the sync manifest.
	Name string

	// Model is the string /api/status reports in its "device" field.
	Model string

	// MaxWidth and MaxHeight are the panel's usable pixel dimensions. Images
	// larger than this are downscaled to fit, preserving aspect ratio.
	//
	// Zero means the panel geometry is not known. shelf deliberately does not
	// guess: the firmware API does not report panel size (see the captured
	// /api/status in device/testdata), and a wrong guess either wastes bytes or
	// throws away detail the panel could have shown. With zero dimensions the
	// optimizer still converts to grayscale and recompresses — both are wins
	// independent of geometry — and simply skips downscaling.
	MaxWidth  int
	MaxHeight int

	// Grayscale converts images to 8-bit gray. The panels are monochrome, so
	// color channels are bytes the device will never display.
	Grayscale bool

	// Dither applies Floyd-Steinberg error diffusion after grayscale
	// conversion. It helps gradients on a panel with few grey levels and hurts
	// text and line art, so it is off by default.
	Dither bool

	// JPEGQuality is the re-encode quality for JPEG images, 1-100.
	JPEGQuality int
}

// builtinProfiles are the profiles shelf ships with.
//
// The dimensions are intentionally absent. spec.md 14.5 lists panel size for
// the X3 and X4 as an open question to be answered by reading the hardware,
// and a hardcoded guess is worse than doing less: at zero the optimizer's
// geometry step is a no-op and everything else still applies. Fill these in
// from a real device, or override per-device in config.toml.
var builtinProfiles = map[string]Profile{
	"x3-v1": {
		Name:        "x3-v1",
		Model:       "X3",
		Grayscale:   true,
		JPEGQuality: 80,
	},
	"x4-v1": {
		Name:        "x4-v1",
		Model:       "X4",
		Grayscale:   true,
		JPEGQuality: 80,
	},
	// A geometry-free profile for callers that want the panel-independent
	// wins (grayscale, recompression, metadata stripping) and nothing else.
	"generic-v1": {
		Name:        "generic-v1",
		Grayscale:   true,
		JPEGQuality: 80,
	},
}

// ProfileNames lists the built-in profiles in a stable order.
func ProfileNames() []string {
	names := make([]string, 0, len(builtinProfiles))
	for name := range builtinProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LookupProfile resolves a profile by name or by device model.
//
// Both "x4" and "x4-v1" resolve to the current x4 profile, because the CLI
// documents --profile=x4 while the manifest records the versioned name.
func LookupProfile(name string) (Profile, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return Profile{}, fmt.Errorf("convert: no profile given")
	}

	if p, ok := builtinProfiles[key]; ok {
		return p, nil
	}
	// Bare model name: pick the highest version for that model.
	if p, ok := latestForModel(key); ok {
		return p, nil
	}
	return Profile{}, fmt.Errorf("convert: unknown profile %q (have: %s)",
		name, strings.Join(ProfileNames(), ", "))
}

// latestForModel finds the newest profile for a model string such as "x4".
func latestForModel(model string) (Profile, bool) {
	var best Profile
	var found bool
	for _, name := range ProfileNames() {
		p := builtinProfiles[name]
		if !strings.EqualFold(p.Model, model) {
			continue
		}
		// ProfileNames is sorted, so a later match is a later version.
		best, found = p, true
	}
	return best, found
}

// ProfileForModel returns the profile matching a /api/status device string.
func ProfileForModel(model string) (Profile, error) {
	if p, ok := latestForModel(strings.ToLower(strings.TrimSpace(model))); ok {
		return p, nil
	}
	return Profile{}, fmt.Errorf("convert: no profile for device model %q", model)
}

// KnowsPanelSize reports whether the profile can downscale.
//
// Callers surface this so a user is told that geometry is being skipped rather
// than silently getting a smaller win than they expected.
func (p Profile) KnowsPanelSize() bool { return p.MaxWidth > 0 && p.MaxHeight > 0 }

// WithPanelSize returns a copy of the profile with panel dimensions applied.
// Configuration overrides a built-in table entry this way.
func (p Profile) WithPanelSize(w, h int) Profile {
	p.MaxWidth, p.MaxHeight = w, h
	return p
}

// identity is the string folded into the optimized-artifact cache key.
//
// Every field that changes the output bytes appears here. A profile whose
// settings are unchanged must produce the same key, because that is what keeps
// an unchanged profile from causing re-uploads.
func (p Profile) identity() string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%t\x00%t\x00%d",
		p.Name, p.Model, p.MaxWidth, p.MaxHeight,
		p.Grayscale, p.Dither, p.JPEGQuality)
}
