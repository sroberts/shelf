# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`shelf` manages an EPUB library on disk and syncs it to a CrossPoint e-reader (Xteink X3/X4) over
the firmware's HTTP + WebSocket API. Go, no cgo, single static binary. `spec.md` is the design
document and the CrossPoint compatibility contract; read the relevant section before changing
anything in `internal/device` or `internal/sync`.

## Commands

```sh
go build -o shelf ./cmd/shelf            # the actual binary
go build ./...                           # compile check only — writes no binary
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
- `internal/convert` — PDF to EPUB, compiled in, cached and quality-assessed.
- `internal/kosync` — KOReader progress sync: document ids, store, and an embedded server.
- `internal/sync` — manifest, planner, executor.
- `internal/tui` — Bubble Tea frontend over the same internal API the CLI uses. `header.go` is the
  always-visible library summary, `detail.go` the right-hand panel for the book under the cursor.
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

Two things the golden frames must stay free of, both learned the hard way. Fixture books carry an
explicit `AddedUnix` (`goldenAdded`), because `Upsert` stamps `time.Now()` on a zero value and the
detail panel prints the date — without it the frames encode the day they were generated and fail at
the next midnight. And `TestMain` pins `time.Local` to UTC, because that same date renders one day
earlier west of Greenwich and the frames would only pass on the machine that wrote them. The
detail panel also defaults to `GraphicsNone`; cover art is opted into by the root model after
terminal detection, so a CI terminal that happens to support sixel cannot inject escape sequences
into a frame.

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

`sync` prepares books before planning (`internal/sync/prepare.go`): PDFs are converted, EPUBs are
optimized for the target panel, and each result is hashed. This runs *before* `Build` so
`plan.go` stays a pure function. Two rules govern the output, and both exist to protect pinning:

- **Identity is the library source.** `LocalBook.Path` and the manifest's `local_path` stay the
  file on disk in the library, never the cache artifact. `LocalBook.UploadPath` carries the bytes.
  Artifact paths move whenever the converter version or profile changes; if that path were the
  identity, the planner would see a new book, upload it beside the old one, and orphan a pinned
  file with someone's reading position in it.
- **The hash describes the bytes actually sent.** That is what makes an unchanged profile produce
  no churn and a changed one re-upload exactly the books it affected.

Optimization applies only where the bytes are an EPUB. A TXT is rendered natively by the firmware
and is not a zip, so running the optimizer over it fails to open the archive — and since a failed
book is dropped rather than fatal, that silently stops every TXT from syncing.


PDF conversion is **compiled in**, not shelled out to: `internal/convert` imports
[decant](https://github.com/sroberts/decant), which reconstructs reflowable EPUB 3 from a
text-layer PDF and ships a `crosspoint` profile whose numbers come from reading the firmware. It is
pure Go, so the static-binary and cross-compile invariants hold. This is the answer to the
dependency question in `spec.md` 14.6 — the best PDF path is no longer a Calibre component.

- **Conversion shells out to nothing.** `decant` is compiled in and reads PDF, which is the only
  format that needs converting: shelf indexes epub, txt, xtc, and pdf, and the firmware renders
  the first three directly. There is no PATH lookup, no subprocess, and no fallback chain. A test
  asserts every preset has a compiled-in implementation, and another runs a conversion with `PATH`
  emptied — reintroducing a subprocess would fail both.
- decant's module version is folded into the cache key, so upgrading it re-converts rather than
  serving artifacts the old version produced. `decantPinnedVersion` must track `go.mod`; a test
  enforces this, because a `go test` binary's build info carries no dependency list.
- Optimizer panel dimensions are filled in **only where they have a source**. The X4 is 480x800
  from decant; the X3 has never been measured and is deliberately zero, which makes downscaling a
  no-op while grayscale and recompression still apply. Do not guess one in.

### The document id is not what the documentation says

`internal/kosync` identifies a book by KOReader's partial MD5. The first sample offset is **0**,
not 256. KOReader computes `lshift(1024, 2*i)` for `i = -1..10` using LuaJIT's bit library, which
masks the shift count to five bits: `-2` becomes `30`, and `1024 << 30` overflows to zero.
CrossPoint's `KOReaderDocumentId.h` header comment documents 256 and contradicts its own
implementation. Getting this wrong produces no error — progress simply never matches. Pinned by
`TestOffsetsMatchKOReader`.

`progress.db` is **not** disposable, unlike `index.db`. The device pushes reading positions there
and keeps no synchronised copy. `shelf user rm` therefore says how many books it is about to
discard before it asks.

`shelf user` opens `progress.db` directly rather than talking to a running server, so accounts can
be managed whether or not `shelf serve` is up. WAL mode plus the store's busy timeout make the
concurrent access safe, and the server sees a new account on its next request — restarting it is
never necessary. `TestStoreToleratesASecondConnection` pins this.

## Not built yet

The SD-card transport, WebDAV, and mDNS discovery are all designed in
`spec.md` but unimplemented. Font stripping is not implemented: dropping a font means removing its
manifest item and every `@font-face` rule referencing it, and a partial job produces an EPUB
`epubcheck` rejects. The TUI runs conversion inside its `tea.Cmd` with no progress shown, so a
large PDF looks like a hang. The spec's Settings screen is blocked upstream by the firmware crash
above.
