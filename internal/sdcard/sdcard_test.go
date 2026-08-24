package sdcard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/sroberts/shelf/internal/device"
	syncpkg "github.com/sroberts/shelf/internal/sync"
)

// The whole point of the package: the executor must be able to drive a card
// without knowing it is not a device.
var _ syncpkg.Transport = (*Volume)(nil)

func newVolume(t *testing.T) *Volume {
	t.Helper()
	v, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return v
}

func upload(t *testing.T, v *Volume, p string, data string) error {
	t.Helper()
	return v.Upload(context.Background(), device.NewPath(p),
		strings.NewReader(data), int64(len(data)), device.UploadOptions{})
}

func mustUpload(t *testing.T, v *Volume, p string, data string) {
	t.Helper()
	if err := upload(t, v, p, data); err != nil {
		t.Fatalf("upload %s: %v", p, err)
	}
}

func TestRoundTrip(t *testing.T) {
	v := newVolume(t)
	ctx := context.Background()

	if err := v.Mkdir(ctx, device.NewPath("/Books/Le Guin")); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	mustUpload(t, v, "/Books/Le Guin/Wizard.epub", "wizard")

	files, err := v.ListRecursive(ctx, device.NewPath("/Books"))
	if err != nil {
		t.Fatalf("ListRecursive: %v", err)
	}
	entry, ok := files[device.NewPath("/Books/Le Guin/Wizard.epub")]
	if !ok {
		t.Fatalf("uploaded file missing from listing: %v", files)
	}
	if entry.Size != 6 {
		t.Errorf("size = %d, want 6", entry.Size)
	}
	if !entry.IsEpub {
		t.Error("IsEpub = false for a .epub")
	}

	if err := v.Move(ctx, device.NewPath("/Books/Le Guin/Wizard.epub"),
		device.NewPath("/Books/Moved/Wizard.epub")); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if err := v.Delete(ctx, device.NewPath("/Books/Moved/Wizard.epub")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v.Mount(), "Books/Moved/Wizard.epub")); !os.IsNotExist(err) {
		t.Error("file survived Delete")
	}
}

// The firmware's cache holds every reading position on the device. Nothing in
// shelf may write there, and with no firmware in between this package is the
// only thing standing in the way.
func TestProtectedPathsAreRefusedByEveryMutator(t *testing.T) {
	v := newVolume(t)
	ctx := context.Background()

	protected := []string{
		"/.crosspoint/settings.json",
		"/Books/.crosspoint/epub_abc/progress.bin",
		"/System Volume Information/x",
		"/XTCache/y",
	}

	for _, p := range protected {
		p := p
		t.Run(p, func(t *testing.T) {
			dp := device.NewPath(p)

			if err := v.Mkdir(ctx, dp); !errors.Is(err, device.ErrProtectedPath) {
				t.Errorf("Mkdir: %v, want ErrProtectedPath", err)
			}
			if err := upload(t, v, p, "x"); !errors.Is(err, device.ErrProtectedPath) {
				t.Errorf("Upload: %v, want ErrProtectedPath", err)
			}
			if err := v.Delete(ctx, dp); !errors.Is(err, device.ErrProtectedPath) {
				t.Errorf("Delete: %v, want ErrProtectedPath", err)
			}
			if err := v.Move(ctx, device.NewPath("/Books/a.epub"), dp); !errors.Is(err, device.ErrProtectedPath) {
				t.Errorf("Move to: %v, want ErrProtectedPath", err)
			}
			if err := v.Move(ctx, dp, device.NewPath("/Books/a.epub")); !errors.Is(err, device.ErrProtectedPath) {
				t.Errorf("Move from: %v, want ErrProtectedPath", err)
			}
		})
	}

	// Nothing may have been created on the way to those refusals.
	for _, name := range []string{".crosspoint", "System Volume Information", "XTCache"} {
		if _, err := os.Stat(filepath.Join(v.Mount(), name)); err == nil {
			t.Errorf("%s was created despite being protected", name)
		}
	}
}

// A walk must not descend into the cache either, or --prune would see every
// progress file as an orphan.
func TestListRecursiveSkipsTheFirmwareCache(t *testing.T) {
	v := newVolume(t)

	mustUpload(t, v, "/Books/real.epub", "a")
	// Written underneath the guard, the way the firmware would have.
	cache := filepath.Join(v.Mount(), "Books", ".crosspoint", "epub_abc")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "progress.bin"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := v.ListRecursive(context.Background(), device.NewPath("/Books"))
	if err != nil {
		t.Fatalf("ListRecursive: %v", err)
	}
	for p := range files {
		if strings.Contains(p.String(), ".crosspoint") {
			t.Errorf("walked into the firmware cache: %s", p)
		}
	}
	if _, ok := files[device.NewPath("/Books/real.epub")]; !ok {
		t.Error("the real book is missing from the listing")
	}
}

func TestPathsCannotEscapeTheMount(t *testing.T) {
	v := newVolume(t)

	// NewPath cleans traversal, so this asserts the composition holds rather
	// than that one layer does.
	for _, p := range []string{"/../outside.epub", "/Books/../../outside.epub"} {
		local, err := v.local(device.NewPath(p))
		if err != nil {
			continue // refused outright, also fine
		}
		if !strings.HasPrefix(local, v.Mount()) {
			t.Errorf("%s escaped the mount: %s", p, local)
		}
	}
}

// A crash mid-write must not leave a file the next run mistakes for complete.
func TestAFailedUploadLeavesNothingBehind(t *testing.T) {
	v := newVolume(t)
	dest := device.NewPath("/Books/broken.epub")

	err := v.Upload(context.Background(), dest,
		iotest_errReader{}, 100, device.UploadOptions{})
	if err == nil {
		t.Fatal("a failing reader produced no error")
	}

	if _, err := os.Stat(filepath.Join(v.Mount(), "Books/broken.epub")); !os.IsNotExist(err) {
		t.Error("a partial upload left a file at the destination")
	}
	// And no temp file either.
	entries, err := os.ReadDir(filepath.Join(v.Mount(), "Books"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("left behind: %s", e.Name())
	}
}

type iotest_errReader struct{}

func (iotest_errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// A short read must not be renamed into place: on FAT32 a truncated file whose
// size happened to match would be indistinguishable from a good one.
func TestAShortUploadIsRejected(t *testing.T) {
	v := newVolume(t)

	err := v.Upload(context.Background(), device.NewPath("/Books/short.epub"),
		strings.NewReader("only-4k-of-this"), 999999, device.UploadOptions{})
	if err == nil {
		t.Fatal("a short read was accepted")
	}
	if _, statErr := os.Stat(filepath.Join(v.Mount(), "Books/short.epub")); !os.IsNotExist(statErr) {
		t.Error("a short upload was placed at the destination")
	}
}

func TestUploadReportsProgress(t *testing.T) {
	v := newVolume(t)

	var last device.Progress
	var calls int
	body := bytes.Repeat([]byte("x"), 200*1024)

	err := v.Upload(context.Background(), device.NewPath("/Books/big.epub"),
		bytes.NewReader(body), int64(len(body)), device.UploadOptions{
			Progress: func(p device.Progress) { calls++; last = p },
		})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if calls < 2 {
		t.Errorf("progress called %d times, want several for a 200 KB file", calls)
	}
	if last.Sent != int64(len(body)) || last.Total != int64(len(body)) {
		t.Errorf("final progress = %d/%d, want %d/%d",
			last.Sent, last.Total, len(body), len(body))
	}
}

// Without this, every accented book on a Mac looks like one the manifest has
// never seen, and the whole library re-uploads on every run.
func TestListingNormalizesToNFC(t *testing.T) {
	v := newVolume(t)

	const nfc = "Brontë.epub"
	nfd := norm.NFD.String(nfc)
	if nfc == nfd {
		t.Fatal("test fixture is not actually decomposed")
	}

	if err := os.MkdirAll(filepath.Join(v.Mount(), "Books"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.Mount(), "Books", nfd), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := v.ListRecursive(context.Background(), device.NewPath("/Books"))
	if err != nil {
		t.Fatalf("ListRecursive: %v", err)
	}
	if _, ok := files[device.NewPath("/Books/"+nfc)]; !ok {
		var got []string
		for p := range files {
			got = append(got, fmt.Sprintf("%q", p))
		}
		t.Errorf("NFC key missing; listing has %v", got)
	}
}

// The executor aborts a whole run on ErrDiskFull rather than retrying. An
// unmapped ENOSPC would retry every remaining book three times against a card
// that has no room for any of them.
func TestOutOfSpaceMapsToDiskFull(t *testing.T) {
	wrapped := fmt.Errorf("sdcard: write /Books/a.epub: %w", syscall.ENOSPC)
	if got := mapError(wrapped); !errors.Is(got, device.ErrDiskFull) {
		t.Errorf("mapError(ENOSPC) = %v, want ErrDiskFull", got)
	}
	if got := mapError(errors.New("something else")); errors.Is(got, device.ErrDiskFull) {
		t.Error("an unrelated error was reported as a full disk")
	}
}

// shelf never plans a directory removal; treating one as an orphan once nearly
// deleted a sync root. Refusing here means a planner bug cannot become data
// loss.
func TestDeleteRefusesADirectory(t *testing.T) {
	v := newVolume(t)
	ctx := context.Background()

	if err := v.Mkdir(ctx, device.NewPath("/Books/Series")); err != nil {
		t.Fatal(err)
	}
	mustUpload(t, v, "/Books/Series/inside.epub", "x")

	if err := v.Delete(ctx, device.NewPath("/Books/Series")); err == nil {
		t.Fatal("Delete removed a directory")
	}
	if _, err := os.Stat(filepath.Join(v.Mount(), "Books/Series/inside.epub")); err != nil {
		t.Errorf("the directory's contents were disturbed: %v", err)
	}
}

func TestStatusSatisfiesTheCompatGate(t *testing.T) {
	v := newVolume(t)

	status, err := v.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Mode != device.ModeLocal {
		t.Errorf("Mode = %q, want %q", status.Mode, device.ModeLocal)
	}
	// A card reports no firmware version. The gate must stand down rather than
	// reject it, or the SD transport can never run.
	if err := device.CheckCompat(status); err != nil {
		t.Errorf("CheckCompat on an SD volume: %v", err)
	}
	// The gate must still bite on a real device that reports nothing.
	if err := device.CheckCompat(&device.Status{Mode: "STA"}); err == nil {
		t.Error("CheckCompat accepted a networked device with no version")
	}

	// CompatWarning needs the same exemption. Without it every SD sync opens
	// with 'device firmware  differs from the tested 1.4.1', which is both
	// alarming and meaningless for a card.
	if w := device.CompatWarning(status); w != "" {
		t.Errorf("CompatWarning on an SD volume: %q", w)
	}
	if w := device.CompatWarning(&device.Status{Mode: "STA", Version: "9.9.9"}); w == "" {
		t.Error("CompatWarning went silent for a networked device on untested firmware")
	}
}

func TestOpenRejectsAMissingMount(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("Open accepted a path that does not exist")
	}
	if _, err := Open(""); err == nil {
		t.Fatal("Open accepted an empty mount")
	}
}

// Writing a library onto the wrong volume is this transport's worst failure.
func TestVerify(t *testing.T) {
	marker := func(t *testing.T, v *Volume, uuid string) {
		t.Helper()
		dir := filepath.Join(v.Mount(), "shelf")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"device_uuid":%q}`, uuid)
		if err := os.WriteFile(filepath.Join(dir, "device.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a fresh empty card is accepted", func(t *testing.T) {
		if err := newVolume(t).Verify("some-uuid"); err != nil {
			t.Errorf("Verify: %v", err)
		}
	})

	t.Run("a card the firmware has touched is accepted", func(t *testing.T) {
		v := newVolume(t)
		if err := os.MkdirAll(filepath.Join(v.Mount(), ".crosspoint"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(v.Mount(), "unrelated.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := v.Verify("some-uuid"); err != nil {
			t.Errorf("Verify: %v", err)
		}
	})

	t.Run("a matching marker is accepted", func(t *testing.T) {
		v := newVolume(t)
		marker(t, v, "uuid-a")
		if err := v.Verify("uuid-a"); err != nil {
			t.Errorf("Verify: %v", err)
		}
	})

	t.Run("a different card is refused", func(t *testing.T) {
		v := newVolume(t)
		marker(t, v, "uuid-b")
		err := v.Verify("uuid-a")
		if err == nil {
			t.Fatal("Verify accepted a card belonging to another device")
		}
		for _, want := range []string{"uuid-a", "uuid-b"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not name %s: %v", want, err)
			}
		}
	})

	t.Run("someone's home directory is refused", func(t *testing.T) {
		v := newVolume(t)
		for _, name := range []string{"Documents", "Desktop", "taxes.pdf"} {
			if err := os.WriteFile(filepath.Join(v.Mount(), name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := v.Verify(""); err == nil {
			t.Fatal("Verify accepted a populated volume with no CrossPoint markings")
		}
	})

	t.Run("a first sync to a marked card without a manifest is accepted", func(t *testing.T) {
		v := newVolume(t)
		marker(t, v, "uuid-a")
		if err := v.Verify(""); err != nil {
			t.Errorf("Verify: %v", err)
		}
	})
}

// A cancelled context must stop the walk rather than finish the card.
func TestContextCancellationStopsWork(t *testing.T) {
	v := newVolume(t)
	mustUpload(t, v, "/Books/a.epub", "x")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := v.ListRecursive(ctx, device.NewPath("/Books")); !errors.Is(err, context.Canceled) {
		t.Errorf("ListRecursive: %v, want context.Canceled", err)
	}
	if err := v.Mkdir(ctx, device.NewPath("/Books/x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Mkdir: %v, want context.Canceled", err)
	}
	if err := v.Upload(ctx, device.NewPath("/Books/b.epub"),
		strings.NewReader("x"), 1, device.UploadOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Upload: %v, want context.Canceled", err)
	}
}

// A missing directory is an empty one, matching the HTTP client, whose 200-[]
// behaviour the planner already depends on.
func TestListingAMissingRootIsEmptyNotAnError(t *testing.T) {
	v := newVolume(t)

	files, err := v.ListRecursive(context.Background(), device.NewPath("/Nope"))
	if err != nil {
		t.Fatalf("ListRecursive: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("files = %v, want empty", files)
	}
}

var _ io.Reader = iotest_errReader{}
