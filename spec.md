# shelf: a Go TUI library manager for CrossPoint e-readers

**Version:** 0.1 (draft spec)
**Target:** Linux first, macOS secondary
**Language:** Go 1.23+
**UI:** charmbracelet/bubbletea

---

## 1. Bottom line

Build a single static Go binary that treats your filesystem as the library and the CrossPoint device as a dumb, path-stable sync target. Files on disk are the source of truth. A SQLite index is a derived cache you can delete at any time. Sync runs over CrossPoint's documented HTTP, WebSocket, and WebDAV endpoints on port 80/81, with a direct SD-card mode for offline use.

Three constraints drive the entire design:

1. **CrossPoint does not read PDF.** The firmware handles `.epub`, `.xtc`/`.xtch`, `.txt`, and `.bmp`. PDF support means local conversion before transfer, not passthrough. See §9.
2. **The device clears its render and progress cache on upload, rename, move, and delete.** Any operation that touches a book's path destroys its reading position. Path stability is therefore a hard requirement, not a nicety. See §6.3.
3. **The device accepts one upload at a time** (`ERROR:Upload already in progress`) and runs on an ESP32-C3 with roughly 380 KB of usable RAM. The sync executor is strictly serial and rate-aware. No parallel transfer.

Everything Calibre gets wrong that this fixes: no library database that owns your files, no directory reshuffling on metadata edits, no `metadata.opf` litter, no GUI dependency, no Qt, no Python runtime, no plugin ABI.

---

## 2. Scope

### In scope

- Import, index, and browse an EPUB library from a plain directory tree
- Read and write EPUB metadata in place (OPF), extract covers
- Full-text-free search across title, author, series, tags, and path
- Named shelves (saved queries or explicit sets) that define what syncs to a device
- Bidirectional device file management: list, upload, download, rename, move, delete, mkdir
- Device settings and font management via the device API
- Reading progress ingest via KOReader sync protocol
- PDF and TXT conversion to EPUB through pluggable external converters
- Device-targeted EPUB optimization (image downscale, recompression)
- Headless CLI mode for scripting and cron

### Out of scope (v1)

- Rendering or reading books in the terminal
- DRM handling of any kind
- OPDS server (the device can already browse OPDS; run `calibre-web` or `kavita` if you want one)
- Windows support
- Multi-user or networked library

---

## 3. CrossPoint compatibility contract

All facts below come from the firmware's `docs/webserver-endpoints.md` and `docs/file-formats.md` on the `develop` branch. Pin the firmware version you test against in `internal/device/compat.go` and gate features on `GET /api/status` → `version`.

### 3.1 Network surface

| Service | Port | Use in shelf |
|---|---|---|
| HTTP | 80 | Status, file listing, download, multipart upload, settings, fonts, OPDS config |
| WebSocket | 81 | Primary upload path with progress frames |
| WebDAV | 80 | Recursive listing via `PROPFIND`, bulk download, `PUT` fallback |
| UDP discovery | 8134 | Send `hello`, receive `crosspoint (on <hostname>);81` |
| mDNS | 5353 | `crosspoint.local` when the network resolves it |

The device serves this only while in File Transfer or Calibre Wireless mode. shelf must detect and report "device asleep or not in transfer mode" as a distinct, non-fatal error rather than a generic timeout.

### 3.2 Endpoints shelf consumes

- `GET /api/status` → `{version, ip, mode, rssi, freeHeap, uptime, device}`. Poll before every sync. `mode` is `STA` or `AP`; `device` is `X3` or `X4` and selects the panel profile for optimization.
- `GET /api/files?path=/Books` → array of `{name, size, isDirectory, isEpub}`. **No mtime and no checksum.** Change detection must not depend on either. Hidden dotfiles are omitted unless the device setting `showHiddenFiles` is on.
- `GET /download?path=…` → file bytes. EPUBs are served as `application/epub+zip`.
- `POST /upload?path=…` → multipart form field `file`. 4 KB device-side write buffer. Overwrites same-named files. Clears the EPUB cache for that path.
- `POST /mkdir` (`name`, `path`), `POST /rename` (`path`, `name`), `POST /move` (`path`, `dest`), `POST /delete` (`path` or `paths` as a JSON array). Rename and move operate on files only, not folders. Delete rejects non-empty folders.
- `GET|POST /api/settings` for device settings, `GET|POST /api/fonts*` for `.cpfont` families, `GET|POST /api/opds*` for saved catalogs.

### 3.3 WebSocket upload protocol (primary path)

```
client -> START:<filename>:<size>:<path>
server -> READY
client -> [binary chunks]
server -> PROGRESS:<received>:<total>     # every 64 KB and at completion
server -> DONE | ERROR:<message>
```

Implementation rules:

- Chunk size 16 KB default, configurable 4 KB to 64 KB. Do not exceed 64 KB; the device is RAM constrained.
- One upload in flight per device, globally. Serialize behind a mutex in the device client, not just in the sync planner.
- On disconnect or error the device deletes the partial file. There is no resume. Retries restart from byte zero; cap at 3 attempts with backoff.
- Map every documented `ERROR:` string to a typed Go error. `ERROR:Write failed - disk full?` aborts the entire sync run, not just the current file.
- Fall back to `POST /upload` if the WebSocket handshake fails, and to WebDAV `PUT` if both fail.

### 3.4 Reserved and protected paths

Never write, rename, move, or delete inside:

- `/.crosspoint/` (device cache: `epub_<hash>/`, `settings.json`, `state.json`, `recent.json`)
- `System Volume Information`
- `XTCache`

shelf treats `/.crosspoint` as read-only and opaque. Do not attempt to write `progress.bin` or any `sections/*.bin`. Those formats are versioned (`book.bin` v7, `section.bin` v30) and change without notice.

### 3.5 Device identity

The status API exposes no serial number or MAC, only `X3` or `X4`. shelf assigns identity itself:

- On first sync, write `/shelf/device.json` containing a generated UUIDv7, the assigned nickname, and the schema version.
- Use a **visible** directory, not a dotfile. The firmware hides dotfiles from listings unless `showHiddenFiles` is enabled and rejects downloads of some protected dotfiles. A visible `/shelf/` folder costs one line in the device's folder browser and avoids that whole class of failure.
- **Open question to verify on hardware:** whether an arbitrary dotfile directory such as `/.shelf/` is readable via `GET /download` when `showHiddenFiles` is on. If it is, offer `--hidden-state` to move the marker.

---

## 4. Local library model

### 4.1 Layout

Default root `~/Books`, configurable. Default on-disk shape:

```
~/Books/
├── Herman Melville/
│   └── Moby-Dick.epub
├── Ursula K. Le Guin/
│   ├── The Dispossessed.epub
│   └── Earthsea 01 - A Wizard of Earthsea.epub
└── _inbox/
    └── something-just-downloaded.epub
```

Rules:

- shelf **never moves or renames a file you did not ask it to move or rename.** Import can place a new file; `shelf organize` can restructure on explicit command. Metadata edits alone never touch paths.
- The directory layout is a rendering template (`{author}/{series} {series_index:02d} - {title}.epub`), applied only at import and on explicit reorganize.
- `_inbox/` is a watched drop directory. `shelf import` promotes files out of it.
- No sidecar files. Metadata lives in the EPUB's OPF. Covers are extracted to the index cache, not written next to the book.

### 4.2 Index

SQLite at `$XDG_DATA_HOME/shelf/index.db`, using `modernc.org/sqlite` (pure Go, no cgo, so NixOS static builds and cross-compiles stay trivial).

```sql
CREATE TABLE books (
  id            INTEGER PRIMARY KEY,
  sha256        TEXT NOT NULL UNIQUE,   -- of the whole file
  path          TEXT NOT NULL UNIQUE,   -- absolute, canonical
  size          INTEGER NOT NULL,
  mtime_unix    INTEGER NOT NULL,
  format        TEXT NOT NULL,          -- epub | txt | xtc | pdf(source only)
  title         TEXT,
  author_sort   TEXT,
  series        TEXT,
  series_index  REAL,
  language      TEXT,
  publisher     TEXT,
  pubdate       TEXT,
  identifiers   TEXT,                   -- JSON object: isbn, uuid, ...
  added_unix    INTEGER NOT NULL,
  cover_blob    BLOB,                   -- extracted thumbnail, nullable
  meta_json     TEXT                    -- full parsed OPF for round-tripping
);
CREATE TABLE tags   (book_id INTEGER, tag TEXT, PRIMARY KEY (book_id, tag));
CREATE TABLE shelves(id INTEGER PRIMARY KEY, name TEXT UNIQUE, query TEXT, kind TEXT);
CREATE TABLE shelf_members(shelf_id INTEGER, book_id INTEGER, PRIMARY KEY (shelf_id, book_id));
CREATE VIRTUAL TABLE books_fts USING fts5(title, author_sort, series, tags, content='books');
```

Rescan is cheap: stat every file, compare `(size, mtime)`, and hash only when they differ. `shelf scan --deep` forces rehash of everything. Deleting `index.db` loses nothing but the shelves table, so back that up separately as TOML in `$XDG_CONFIG_HOME/shelf/shelves.toml` and treat SQLite as pure cache.

### 4.3 Metadata handling

- Parse `META-INF/container.xml` → OPF path → Dublin Core fields, `<meta>` refinements (EPUB 3 `belongs-to-collection` for series, `calibre:series` legacy meta for compatibility with existing libraries).
- Write back by rewriting the OPF entry inside the zip and repacking with `archive/zip`, preserving the `mimetype` entry first and stored (not deflated). Getting this wrong breaks EPUB validity, so it needs a dedicated test with a golden file.
- Cover extraction: OPF `<meta name="cover">` → manifest item, or the `properties="cover-image"` item in EPUB 3. Downscale to 256 px wide PNG for the index.
- Never rewrite a file just to normalize metadata during sync. Metadata writes are an explicit user action.

---

## 5. Package layout

```
cmd/shelf/main.go               # flag parsing, mode dispatch (TUI vs CLI)
internal/config                 # TOML load, XDG paths, defaults
internal/epub                   # container/OPF/NCX parse, cover extract, repack
internal/library                # scan, index, search, shelves, import, organize
internal/device
    client.go                   # HTTP client, typed errors, retry policy
    discover.go                 # UDP 8134 + mDNS + static host
    ws.go                       # WebSocket upload state machine
    webdav.go                   # PROPFIND recursive listing, PUT/GET fallback
    settings.go, fonts.go, opds.go
    compat.go                   # firmware version gating
internal/sync
    manifest.go                 # per-device state, path pinning
    plan.go                     # pure diff: (local set, device set, manifest) -> []Op
    exec.go                     # serial executor, progress events
internal/convert                # pdf->epub, txt->epub, image optimize
internal/kosync                 # KOReader progress sync client/server
internal/tui
    root.go                     # top-level tea.Model, focus routing
    library.go, detail.go, device.go, syncview.go, settings.go
    keys.go, styles.go, help.go
```

Pure functions in `sync/plan.go` and `epub` carry the test weight. `exec.go` and the TUI stay thin.

---

## 6. Sync engine

### 6.1 State

Per device, stored locally at `$XDG_STATE_HOME/shelf/devices/<uuid>.json` and mirrored to `/shelf/manifest.json` on the device so a second machine can reconstruct state:

```json
{
  "device_uuid": "0192...",
  "nickname": "x4",
  "model": "X4",
  "firmware": "1.0.0",
  "root": "/Books",
  "last_sync_unix": 1753900000,
  "entries": [
    {
      "sha256": "…",
      "device_path": "/Books/Ursula K. Le Guin/The Dispossessed.epub",
      "device_size": 842114,
      "local_path": "/home/scott/Books/Ursula K. Le Guin/The Dispossessed.epub",
      "uploaded_unix": 1753800000,
      "optimized": true,
      "optimize_profile": "x4-v1"
    }
  ]
}
```

`sha256` is the hash of the **bytes actually sent** (post-optimization), not the local source, so an unchanged optimizer profile produces no churn.

### 6.2 Change detection

The device listing gives you name, size, and directory flag. Nothing else. The decision table:

| Manifest entry | Device file at pinned path | Size match | Action |
|---|---|---|---|
| present | present | yes | no-op |
| present | present | no | re-upload (overwrite in place) |
| present | absent | n/a | re-upload, or drop from shelf if user deleted it on device (`--respect-device-deletes`) |
| absent | present | n/a | orphan: report, delete only with `--prune` |
| present, local file gone | present | n/a | delete from device on `--prune`, otherwise warn |

Size collisions are theoretically possible and practically irrelevant here. `shelf sync --verify` adds a `GET /download` and rehash pass for the paranoid, at the cost of pulling every byte back over Wi-Fi.

### 6.3 Path pinning (the important part)

Once a book occupies a device path, that path is frozen in the manifest. Later changes to local metadata, series numbering, or the naming template do **not** move it. Rationale: `/rename`, `/move`, and `/upload` all clear the device's `.crosspoint/epub_<hash>` cache, which contains `progress.bin`. Reshuffling paths silently destroys reading positions across the whole library, which is exactly the Calibre behavior you are trying to escape.

Repathing happens only under `shelf sync --repath`, which prints an explicit warning listing every book that will lose its reading position, and refuses to run non-interactively without `--yes`.

### 6.4 Execution

Single goroutine, ordered: `mkdir` operations first (deepest-last), then uploads smallest-first (fail fast on a full card), then deletes. Each op emits a progress event over a channel consumed by `tea.Program.Send`. Between uploads, sleep the configured `inter_op_delay` (default 150 ms) to let the ESP32 flush its SD writes.

Abort conditions: disk-full error, three consecutive upload failures, device status poll failure, user interrupt (which finishes the current file, then stops cleanly).

### 6.5 SD-card transport

`--transport=sd --mount=/run/media/scott/CROSSPOINT` uses the same planner with a filesystem-backed device implementation. Same manifest, same path pinning. Rules for FAT32 targets: reject characters `<>:"/\|?*`, cap component length at 255 bytes, detect case-insensitive collisions before writing, and refuse to touch `/.crosspoint`.

---

## 7. TUI design

### 7.1 Architecture

Standard Elm loop. One root `tea.Model` owning a focus enum and a slice of child models. Rules:

- No blocking I/O in `Update`. Every device call and every disk scan returns a `tea.Cmd`.
- Long-running work (sync, scan, convert) runs in its own goroutine and pushes messages with `p.Send(...)`. Cancellation via `context.Context` stored in the model.
- Log to `$XDG_STATE_HOME/shelf/shelf.log`, never to stdout, since stdout is the display.
- Support `--no-tui` for every operation the TUI can perform. The TUI is a frontend on the same internal API, not a place where logic lives.

### 7.2 Screens

1. **Library** (default): sortable, filterable table of books. Columns: title, author, series, size, sync state per active device (a single glyph column: synced, pending, absent, orphan).
2. **Detail**: metadata for one book, editable fields, cover as ASCII or sixel/kitty graphics when the terminal supports it, list of device placements.
3. **Devices**: discovered and configured devices, status line (`freeHeap`, `rssi`, `mode`, firmware), free space, book count.
4. **Device browser**: remote file tree over `/api/files`, with rename, move, delete, and download bound to keys. Reuses the local table component.
5. **Sync**: plan preview (adds, updates, deletes, bytes total) requiring confirmation, then a live progress view driven by `PROGRESS:` frames.
6. **Convert queue**: pending PDF and TXT conversions with per-item status and stderr tail on failure.
7. **Settings**: device settings pulled from `GET /api/settings`, rendered by declared type (`toggle`, `enum`, `value`, `string`) so new firmware settings appear without a shelf release.

### 7.3 Keymap

Vim-first, discoverable through `bubbles/help`.

| Key | Action |
|---|---|
| `j`/`k`, `g`/`G`, `ctrl+d`/`ctrl+u` | Navigate |
| `/` | Filter (FTS5 query passthrough) |
| `tab`/`shift+tab` | Cycle screens |
| `space` | Toggle selection |
| `v` | Visual range select |
| `e` | Edit metadata |
| `s` | Sync selected to active device |
| `S` | Sync active shelf |
| `d` | Delete (local or device, contextual; always confirms) |
| `p` | Pull from device |
| `c` | Convert to EPUB |
| `o` | Optimize for active device |
| `1`-`9` | Jump to shelf |
| `?` | Help |
| `q`, `ctrl+c` | Quit (graceful: cancels context, waits for current upload) |

### 7.4 Dependencies

```
github.com/charmbracelet/bubbletea
github.com/charmbracelet/bubbles        // list, table, textinput, viewport, progress, key, help
github.com/charmbracelet/lipgloss
modernc.org/sqlite                      // cgo-free
github.com/coder/websocket              // successor to nhooyr.io/websocket
github.com/studio-b12/gowebdav
github.com/grandcat/zeroconf            // mDNS; optional, UDP 8134 covers most cases
github.com/BurntSushi/toml
golang.org/x/text                       // unicode normalization for filenames
golang.org/x/image                      // cover and optimizer resampling
```

Keep the dependency list at roughly this size. Every addition is a NixOS packaging problem later.

---

## 8. CLI surface

Every verb works headless and returns useful exit codes. This is what makes the tool scriptable in a way Calibre never was for you.

```
shelf scan [--deep]
shelf import <path>...              [--template=…] [--move|--copy|--link]
shelf ls [query] [--json]
shelf meta <book> [--set title=…] [--set series=…]
shelf shelf create <name> --query 'tag:queue and not tag:done'
shelf devices [--discover]
shelf sync [<shelf>] [--device=x4] [--dry-run] [--prune] [--verify] [--repath] [--yes]
shelf pull [--progress-only]
shelf push <file>... --to /Books
shelf convert <file.pdf> [--profile=x4]
shelf optimize <file.epub> --profile=x4
shelf settings get|set <key> [value]
shelf fonts install <family> <file.cpfont>
shelf doctor                        # connectivity, firmware compat, cache sanity
```

`--json` on read commands emits newline-delimited JSON for piping into `jq`.

---

## 9. Format handling

### 9.1 The PDF problem, stated plainly

CrossPoint renders EPUB 2/3, TXT, BMP, and the Xteink native `.xtc`/`.xtch` container. It has no PDF engine. On a 4-to-7 inch e-ink panel a reflowed EPUB beats a scaled PDF page anyway, so conversion is the right answer, not a workaround. shelf therefore treats PDF as a **source format only**: PDFs live in the library, are indexed, and are converted on the way to the device. The manifest records the conversion so the source and the derived EPUB stay linked.

Conversion is a pluggable external command, configured in TOML, with these presets:

| Preset | Command | Best for |
|---|---|---|
| `ebook-convert` | `ebook-convert in.pdf out.epub --enable-heuristics` | Text PDFs; highest fidelity available. Ships in nixpkgs `calibre` as a CLI binary, no GUI required |
| `pandoc` | `pandoc in.pdf -o out.epub` | Simple, text-only documents |
| `mutool` | `mutool convert -o out.html in.pdf` plus internal EPUB assembly | Fast, fewest dependencies |
| `ocr` | `ocrmypdf` then one of the above | Scanned documents |

Be honest in the UI about what conversion produces: reflowable text PDFs convert well, two-column academic papers convert poorly, and scanned page images convert to garbage without OCR. For fixed-layout material that must keep its layout, offer a `--rasterize` path that emits per-page BMPs into a device folder, which the firmware can display natively.

Yes, the best PDF converter available is a Calibre component. Using `ebook-convert` as a headless subprocess is not the same as living in Calibre, and it stays swappable.

### 9.2 EPUB optimization

Mirror what the device's built-in EPUB Optimizer does, but locally, where you have CPU to spare:

- Downscale images to the target panel dimensions from the device profile (read `device` from `/api/status`, keep the pixel dimensions in a config table rather than hardcoding them, so a new supported device is a config change).
- Convert images to 8-bit grayscale, dither optionally.
- Recompress and drop metadata blobs.
- Optionally strip embedded fonts, since the device has its own font stack and SD-card `.cpfont` families.
- Preserve the original in the library. Optimized output is a derived artifact keyed by `(sha256, profile)` and cached in `$XDG_CACHE_HOME/shelf/optimized/`.

---

## 10. Reading progress

CrossPoint supports KOReader progress sync. Use that protocol rather than parsing `progress.bin`, whose format is undocumented and whose parent `section.bin` is already at version 30.

- `internal/kosync` implements the standard kosync HTTP API: `POST /users/create`, `GET /users/auth`, `PUT /syncs/progress`, `GET /syncs/progress/<document>`, with `X-Auth-User` and `X-Auth-Key` (MD5 of the password) headers.
- shelf can act as a **client** against an existing kosync server and display percentage read in the library table.
- Stretch goal: shelf runs an embedded kosync server (`shelf serve --kosync :8080`) so you own the whole loop with no third-party account. Point the device's sync settings at it.
- Document matching uses KOReader's partial-MD5 document hash, not the file hash. Implement it exactly (samples at powers-of-two offsets, 1024 bytes each) or progress will never match.

---

## 11. Configuration

`$XDG_CONFIG_HOME/shelf/config.toml`:

```toml
library_root = "~/Books"
inbox = "~/Books/_inbox"
naming_template = "{author}/{series} {series_index:02d} - {title}"

[ui]
theme = "auto"          # auto | light | dark
graphics = "auto"       # auto | kitty | sixel | ascii | none

[[device]]
nickname   = "x4"
host       = "crosspoint.local"    # empty triggers discovery
root       = "/Books"
transport  = "ws"                  # ws | http | webdav | sd
chunk_size = 16384
optimize   = true
profile    = "x4-v1"

[convert]
pdf = "ebook-convert"
timeout = "10m"

[sync]
inter_op_delay = "150ms"
max_retries = 3
prune = false
```

---

## 12. Error handling and testing

Typed errors, mapped from the device's exact strings:

```go
var (
    ErrUploadInProgress = errors.New("device: upload already in progress")
    ErrDiskFull         = errors.New("device: SD write failed, likely full")
    ErrProtectedPath    = errors.New("device: protected path")
    ErrNotInTransfer    = errors.New("device: not in file transfer mode")
)
```

Test strategy:

- `internal/device`: an `httptest` server plus a fake WebSocket peer that replays the documented `READY`/`PROGRESS`/`DONE`/`ERROR` sequences, including every failure string.
- `internal/sync/plan.go`: pure table-driven tests over the §6.2 decision matrix. This is the highest-value test surface in the project.
- `internal/epub`: golden-file round-trip tests proving that metadata write-back keeps the archive valid, with `mimetype` first and stored.
- TUI: `teatest` from `charmbracelet/x/exp/teatest` for golden-frame regression on the library and sync views.
- One integration target guarded by a build tag that runs against real hardware.

---

## 13. Milestones

| Milestone | Deliverable | Definition of done |
|---|---|---|
| M0 | `internal/epub` and `internal/library` | `shelf scan` indexes a real library and `shelf ls` searches it |
| M1 | `internal/device` HTTP and discovery | `shelf devices --discover` and `shelf doctor` report a live X3/X4 |
| M2 | WebSocket uploads plus `internal/sync` | `shelf sync --dry-run` and a real one-way push with correct path pinning |
| M3 | Bubble Tea shell: library, device, sync views | Full push workflow without touching the CLI |
| M4 | Convert and optimize pipelines | PDF in, readable EPUB on device |
| M5 | kosync client, then embedded server | Reading percentage visible in the library table |
| M6 | Packaging | Nix flake, static `x86_64-linux` binary, `nix run` works |

M0 through M2 are the actual product. Everything after is refinement.

---

## 14. Risks and open questions

1. **Firmware API drift.** CrossPoint is under heavy development (1,000+ commits, active PR queue). Pin a tested version in `compat.go`, gate on the `version` string, and fail loudly on unknown majors rather than guessing.
2. **Dotfile marker readability.** Verify on hardware whether `/.shelf/` is downloadable when `showHiddenFiles` is enabled. Default to the visible `/shelf/` folder until proven otherwise.
3. **No device-side hashing.** Verification requires downloading the file back. Accept size-based detection as the default and make `--verify` opt-in.
4. **`.xtc`/`.xtch` is undocumented here.** Treat it as an opaque passthrough format: index it, sync it, never parse or generate it.
5. **Panel dimensions for X3 and X4** are not in the docs reviewed. Read them off the hardware and put them in the profile config table rather than hardcoding a guess.
   **Half answered.** X4 is 480x800, taken from the CrossPoint firmware by way of `decant`'s
   crosspoint profile, and is in `x4-v1`. **X3 is still unread** and `x3-v1` deliberately carries
   zero: at zero the optimizer skips geometry and still applies grayscale and recompression, which
   is strictly better than downscaling to a guessed size and discarding detail the panel could have
   shown. Fill it in from hardware, not from a product page.
6. **`ebook-convert` dependency** reintroduces a Calibre component as the best PDF path. It is a subprocess and it is swappable, but it is a dependency. Decide now whether that is acceptable or whether `mutool` plus a hand-rolled assembler is worth the effort.
   **Answered: neither.** The PDF path is `decant`, a pure-Go library compiled in, so a PDF converts
   with nothing on `PATH` — no Calibre, no Java, no `mutool`. Every external converter and the whole
   subprocess path were removed rather than kept as fallbacks: shelf indexes four formats and the
   firmware renders three of them, so PDF is the only one that ever needs converting. The external
   converters were carrying formats shelf cannot index, in exchange for a `PATH` dependency, a
   subprocess lifetime, and an injection surface.
7. **Wi-Fi throughput on an ESP32-C3 is modest.** A large library's first sync will take a long time. Make the SD-card transport a first-class path, not an afterthought, for bulk initial loads.

---

## 15. Source references

- CrossPoint firmware: <https://github.com/crosspoint-reader/crosspoint-reader>
- Webserver endpoints: `docs/webserver-endpoints.md`
- On-device cache formats: `docs/file-formats.md`
- User guide and scope: `USER_GUIDE.md`, `SCOPE.md`
- Bubble Tea: <https://github.com/charmbracelet/bubbletea>