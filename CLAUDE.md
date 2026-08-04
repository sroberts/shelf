# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`shelf` manages an EPUB library on disk and syncs it to a CrossPoint e-reader (Xteink X3/X4) over
the firmware's HTTP + WebSocket API. Go, no cgo, single static binary. `spec.md` is the design
document and the CrossPoint compatibility contract; read the relevant section before changing
anything in `internal/device` or `internal/sync`.

## Commands

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...                      # what CI runs
gofmt -l .                               # must print nothing; CI fails on any output

go test ./internal/sync/                 # one package
go test ./internal/sync/ -run TestPathPinning -v
```

Golden TUI frames (`internal/tui/testdata/*.golden`) are regenerated, never hand-edited:

```sh
go test ./internal/tui/ -update-golden
```

Hardware tests are build-tagged and skip without a device:

```sh
SHELF_TEST_DEVICE=crosspoint.local go test -tags hardware ./internal/...
```

`internal/epub` runs `epubcheck` as an independent validator when it is on `PATH`, and skips
otherwise. Installing it (`brew install epubcheck`) is worth it when touching `epub/write.go`.

Run tests from the repo root — several use `testdata/` via relative paths.

## Architecture

Data flows one direction: **files on disk → index → planner → executor → device.** Nothing flows
back the other way; the device is a dumb, path-stable target.

- `internal/epub` — parse and rewrite EPUB metadata in place. Read-lenient, write-conservative.
- `internal/library` — SQLite index, scan, search, shelves, import, naming templates.
- `internal/device` — HTTP client, discovery, WebSocket upload, firmware gating.
- `internal/convert` — PDF/TXT to EPUB via external converters, cached and quality-assessed.
- `internal/sync` — manifest, planner, executor.
- `internal/tui` — Bubble Tea frontend over the same internal API the CLI uses.
- `cmd/shelf` — flag parsing and dispatch. Bare `shelf` opens the TUI.

### The three invariants

Everything else is detail. These are the reasons the project exists, and breaking one silently
destroys user data.

**1. Path pinning.** Once a book occupies a device path, that path is frozen in the manifest.
`internal/sync/plan.go` takes the destination from the manifest entry, *never* from the current
naming template. Every rename, move, and re-upload clears the firmware's `.crosspoint` cache for
that book, which destroys its reading position. Recomputing a destination for an already-pinned
book is the single worst bug you can introduce here. Repathing happens only under an explicit
`--repath`, which enumerates every book that will lose its place.

**2. The index is a cache.** `index.db` is derived from files on disk and may be deleted at any
time. The only state that cannot be reconstructed is shelf definitions, which is why they are
written to `shelves.toml` *first* and mirrored into SQLite second.

**3. Uploads are serialized in the client, not the caller.** The firmware accepts exactly one
upload at a time. The mutex lives in `device.Client` so no future concurrent planner can violate
it. The executor is serial by design, not by omission.

### Where the design pressure comes from

The device is an ESP32-C3 with ~380 KB usable RAM on Wi-Fi, and its API has real gaps:

- **No mtime, no checksum** in `/api/files`. Change detection is size-based, deliberately. Do not
  build anything that assumes otherwise — there is a test asserting the fields stay absent.
- **A missing directory returns `200 []`, not `404`.** Stat the parent to test existence.
- **No resume.** The device deletes a partial file on disconnect, so a retry restarts at byte zero.
- **Dotfiles are hidden** from listings, which is why shelf's own state lives in a visible `/shelf/`.
- **`GET /api/settings` crashes firmware 1.4.1** and leaves the SD card unmounted. Nothing calls it;
  see `SettingsEndpointUnsafe` in `internal/device/compat.go` and upstream issue #2737.

`internal/device/testdata/` holds responses captured from real hardware, not hand-written JSON,
with the IP and filenames anonymized. When firmware and fixtures disagree, re-capture rather than
editing by hand.

### Testing shape

`sync/plan.go` is a **pure function** — no I/O, no clock, no randomness — which is what makes the
spec's decision table directly testable and guarantees `--dry-run` describes exactly what a real
run does. Keep it that way: if you need something in the planner, add it to `Input`.

`internal/device` tests run against a fake device (`fakedevice_test.go`) and a fake WebSocket peer
replaying every documented `ERROR:` string. Golden TUI frames compare ANSI-stripped output, since
color depends on terminal detection and would fail in CI for reasons unrelated to layout.

## Conventions

- Device paths are `device.Path` (always forward-slash), local paths are `path/filepath`. The
  distinct type exists so the compiler catches a `filepath.Join` on a device path.
- Normalize paths and metadata to **NFC** before they touch the index or a manifest. APFS returns
  NFD, Linux usually NFC; without this a library scanned on both machines grows duplicate rows.
- Case-fold when detecting collisions. The device's SD card is FAT32 and case-insensitive
  regardless of host.
- Map new firmware error strings to typed errors in `device/errors.go` rather than matching text
  at call sites.
- Comments explain *why*, especially where the code looks odd because the firmware is odd.

## Anti-patterns

- **Never move a pinned book** outside an explicit `--repath`.
- **Never plan a directory deletion.** `--prune` removes files only; treating a directory as an
  orphan once nearly deleted the sync root.
- **Never write inside `/.crosspoint`, `System Volume Information`, or `XTCache`.** The guard is in
  `device/path.go` at the lowest layer so callers cannot route around it — keep it there.
- **No blocking I/O in a Bubble Tea `Update`.** Every device call and disk scan returns a `tea.Cmd`.
- **No writes to stdout/stderr from the TUI.** Those belong to the renderer; log to
  `$XDG_STATE_HOME/shelf/shelf.log`.
- **No mutexes in Bubble Tea models.** They are passed by value, so the lock is copied and protects
  nothing. Use channels drained by a `tea.Cmd`.
- Dependencies are kept deliberately small (each one is a future packaging problem). Ask first.

## Conversion and optimization

PDF conversion is **compiled in**, not shelled out to: `internal/convert` imports
[decant](https://github.com/sroberts/decant), which reconstructs reflowable EPUB 3 from a
text-layer PDF and ships a `crosspoint` profile whose numbers come from reading the firmware. It is
pure Go, so the static-binary and cross-compile invariants hold. This is the answer to the
dependency question in `spec.md` 14.6 — the best PDF path is no longer a Calibre component.

- `decant` is the default and reads PDF only. `NewForFile` falls back to `ebook-convert`, then
  `pandoc`, for the formats it does not read. An explicit `--converter` is honored strictly.
- decant's module version is folded into the cache key, so upgrading it re-converts rather than
  serving artifacts the old version produced. `decantPinnedVersion` must track `go.mod`; a test
  enforces this, because a `go test` binary's build info carries no dependency list.
- Optimizer panel dimensions are filled in **only where they have a source**. The X4 is 480x800
  from decant; the X3 has never been measured and is deliberately zero, which makes downscaling a
  no-op while grayscale and recompression still apply. Do not guess one in.

## Not built yet

KOReader progress sync, the SD-card transport, WebDAV, and mDNS discovery are all designed in
`spec.md` but unimplemented. Conversion and optimization are wired to `shelf convert` and
`shelf optimize` but not yet into `sync`, so PDFs are still skipped on the way to a device. Font
stripping is not implemented: dropping a font means removing its manifest item and every
`@font-face` rule referencing it, and a partial job produces an EPUB `epubcheck` rejects. The
spec's Settings screen is blocked upstream by the firmware crash above.
