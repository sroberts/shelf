package epub

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEpubCheckValidatesEditedFile runs the reference EPUB validator against a
// file shelf has edited.
//
// Every other test in this package checks archive validity using shelf's own
// parser, which cannot catch a mistake shelf makes consistently in both
// directions. epubcheck is the only independent authority available, so run it
// when it is installed. It is optional rather than required because it is a
// Java tool and shelf should stay buildable and testable without a JVM.
//
// Install with `brew install epubcheck` or `nix run nixpkgs#epubcheck`.
func TestEpubCheckValidatesEditedFile(t *testing.T) {
	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		t.Skip("epubcheck not installed; skipping independent validation")
	}

	for _, tc := range []struct {
		name string
		make func(*testing.T) *builder
	}{
		{"epub3", epub3},
		{"epub2", epub2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.make(t).write(t)

			patch := Patch{
				Title:       strptr("Edited by shelf"),
				Series:      strptr("Test Series"),
				SeriesIndex: f64ptr(2),
				Subjects:    &[]string{"Fiction"},
				Authors:     &[]Creator{{Name: "A. Writer", FileAs: "Writer, A."}},
			}
			if err := Write(path, patch); err != nil {
				t.Fatal(err)
			}

			out, err := exec.Command(bin, path).CombinedOutput()
			if err == nil {
				return
			}
			// epubcheck exits non-zero on warnings as well as errors; only
			// genuine ERROR/FATAL lines should fail the test.
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "ERROR") || strings.HasPrefix(line, "FATAL") {
					t.Errorf("epubcheck rejected the edited file:\n%s", out)
					return
				}
			}
		})
	}
}
