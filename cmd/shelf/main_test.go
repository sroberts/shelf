package main

import (
	"flag"
	"reflect"
	"testing"
)

// The standard flag package stops at the first positional argument, which would
// make `shelf ls earthsea --json` silently ignore --json and fold it into the
// search query. That silent failure is the bug this guards against.
func TestPermuteArgsAllowsTrailingFlags(t *testing.T) {
	newFS := func() *flag.FlagSet {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.Bool("json", false, "")
		fs.Bool("deep", false, "")
		fs.String("sort", "", "")
		fs.Int("limit", 0, "")
		return fs
	}

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "flag after positional",
			args: []string{"earthsea", "--json"},
			want: []string{"--json", "earthsea"},
		},
		{
			name: "value flag after positional",
			args: []string{"earthsea", "--sort", "title"},
			want: []string{"--sort", "title", "earthsea"},
		},
		{
			name: "equals form needs no lookahead",
			args: []string{"earthsea", "--sort=title"},
			want: []string{"--sort=title", "earthsea"},
		},
		{
			name: "already in order is unchanged",
			args: []string{"--json", "earthsea"},
			want: []string{"--json", "earthsea"},
		},
		{
			name: "interleaved",
			args: []string{"--json", "tag:queue", "--limit", "5", "extra"},
			want: []string{"--json", "--limit", "5", "tag:queue", "extra"},
		},
		{
			name: "bool flag does not swallow the next argument",
			args: []string{"--deep", "somepath"},
			want: []string{"--deep", "somepath"},
		},
		{
			name: "double dash ends flag parsing",
			args: []string{"--json", "--", "--not-a-flag"},
			want: []string{"--json", "--not-a-flag"},
		},
		{
			name: "single dash forms work too",
			args: []string{"earthsea", "-json"},
			want: []string{"-json", "earthsea"},
		},
		{
			name: "no arguments",
			args: nil,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := permuteArgs(newFS(), tt.args)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("permuteArgs(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// The permutation must survive an actual parse, with positionals intact.
func TestParseFlagsEndToEnd(t *testing.T) {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	sortBy := fs.String("sort", "", "")

	if err := parseFlags(fs, []string{"tag:queue", "--json", "--sort", "title"}); err != nil {
		t.Fatal(err)
	}
	if !*asJSON {
		t.Error("--json after a positional was not applied")
	}
	if *sortBy != "title" {
		t.Errorf("--sort = %q, want title", *sortBy)
	}
	if got := fs.Args(); !reflect.DeepEqual(got, []string{"tag:queue"}) {
		t.Errorf("positional args = %v, want [tag:queue]", got)
	}
}

func TestLookupAndSuggest(t *testing.T) {
	if lookup("ls") == nil {
		t.Error("ls should be a known command")
	}
	if lookup("nosuchcommand") != nil {
		t.Error("unknown commands must not resolve")
	}
	if got := closest("sca"); got != "scan" {
		t.Errorf("closest(%q) = %q, want scan", "sca", got)
	}
}

func TestBuildPatch(t *testing.T) {
	p, err := buildPatch(multiFlag{"title=New Title", "series=Earthsea", "series_index=2", "tags=a, b ,c"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Title == nil || *p.Title != "New Title" {
		t.Errorf("Title = %v", p.Title)
	}
	if p.Series == nil || *p.Series != "Earthsea" {
		t.Errorf("Series = %v", p.Series)
	}
	if p.SeriesIndex == nil || *p.SeriesIndex != 2 {
		t.Errorf("SeriesIndex = %v", p.SeriesIndex)
	}
	if p.Subjects == nil || !reflect.DeepEqual(*p.Subjects, []string{"a", "b", "c"}) {
		t.Errorf("Subjects = %v", p.Subjects)
	}

	// Fields that were not set must stay nil so they are left untouched.
	if p.Publisher != nil || p.Language != nil || p.Authors != nil {
		t.Error("unset fields should remain nil")
	}

	for _, bad := range []string{"title", "nosuchfield=x", "series_index=abc", "series_index="} {
		if _, err := buildPatch(multiFlag{bad}); err == nil {
			t.Errorf("buildPatch(%q) should have failed", bad)
		}
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tt := range tests {
		if got := humanSize(tt.in); got != tt.want {
			t.Errorf("humanSize(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 20); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := truncate("a very long title indeed", 10); len([]rune(got)) != 10 {
		t.Errorf("truncate produced %d runes: %q", len([]rune(got)), got)
	}
	// Multi-byte characters must not be split.
	if got := truncate("海辺のカフカという長い題名", 5); len([]rune(got)) != 5 {
		t.Errorf("got %q (%d runes)", got, len([]rune(got)))
	}
}
