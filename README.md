# shelf

A terminal library manager for [CrossPoint](https://github.com/crosspoint-reader/crosspoint-reader)
e-readers (Xteink X3 / X4).

Your filesystem is the library. The device is a path-stable sync target. The SQLite index is a
derived cache you can delete at any time.

## Why

- **No database owns your files.** Books live in a plain directory tree you can read with `ls`.
- **Metadata edits never move files.** Once a book has a path on the device, that path is frozen.
- **Reading positions survive.** The device clears its render and progress cache on any upload,
  rename, or move, so shelf treats path stability as a hard requirement rather than a preference.
- No `metadata.opf` litter, no GUI dependency, no Qt, no Python runtime.

## Status

M0–M6 are implemented and tested. M0–M4 are verified against real hardware (an X4 on firmware
1.4.1). The M5 and M6 device round trips are not yet confirmed — the OPDS catalog is verified
against a port of the firmware's own feed parser rather than against the panel.

| Milestone | Scope | State |
|---|---|---|
| M0 | EPUB parsing, library index, scan and search | done |
| M1 | Device HTTP client and discovery | done |
| M2 | WebSocket upload and the sync engine | done |
| M3 | Terminal interface: library, devices, sync | done |
| M4 | PDF conversion and device-targeted optimization | done |
| M5 | Reading-progress sync, client and embedded server | done |
| M6 | OPDS catalog served from `shelf serve` | done |

The SD-card transport, WebDAV, and mDNS discovery are designed in `spec.md` but not yet built. The spec's Settings screen is blocked upstream — see the firmware
note below.

## Reading progress

The reader ships a KOReader sync client, so shelf runs the other end of it:

```sh
shelf user add scott    # prompts for a password, prints what to type into the reader
shelf serve             # prints the LAN address to point the reader at
```

Accounts can also be registered from the reader's own sync settings, but the reader gives no
sign of which action it took — a Login against an empty server and a wrong password both surface
as "Authentication failed". Creating the account here removes that ambiguity. `shelf user ls`,
`passwd`, and `rm` manage them afterwards, and all four work while `shelf serve` is running: the
server picks up the change on the next request, with no restart. Once your accounts exist, run
the server with `--no-registration`. Set `[kosync] user` in the config and `shelf ls` grows a
READ column.

## Browsing the library from the reader

`shelf serve` also publishes the library as an OPDS catalog, on the same address and the same
account, so the reader can pull a book over Wi-Fi without a sync run:

```
http://<this-machine>:8080/opds
```

Add it under the reader's OPDS settings. You get the whole library, recently added, and browse by
author, series, tag, and shelf, with search wired to the same query syntax as `shelf ls`. Any
other OPDS client works too — Panels, KOReader, Thorium.

Two limits are the firmware's rather than shelf's, and are worth knowing before they surprise you:

- **Only EPUBs are downloadable on CrossPoint.** Its acquisition check is an exact string match on
  `application/epub+zip`, so PDF, TXT, and XTC entries are parsed and then ignored. They are still
  advertised with their real types, because other clients take them — shelf will not mislabel a
  PDF to sneak it past.
- **A book pulled over OPDS is not tracked in the sync manifest.** The firmware picks the
  filename and folder, so the file is not at shelf's pinned path and `shelf sync --prune` sees it
  as an orphan. Pick one route per book, or leave `--prune` off.

`--open-catalog` drops the password, `--no-opds` turns the catalog off entirely, and the `[opds]`
config section sets a title, a page size, and either switch permanently.

If the reader reports an authentication failure, check the scheme first. shelf serves plain
HTTP; an `https://` URL fails the TLS handshake before any credentials are read, and the device
reports that as a bad login.

Two things to know. The protocol runs over plain HTTP and sends both `MD5(password)` and HTTP
Basic with the password itself, so use a credential that is unique and disposable. And
`progress.db` lives under the data directory rather than the cache — unlike `index.db` it cannot
be rebuilt, because the reader pushes positions here and keeps no synchronised copy.

## Conversion needs nothing installed

The firmware has no PDF engine, so shelf converts PDFs on the way to a device. That conversion
runs **in-process**: no Calibre, no Java, no `mutool`, nothing on `PATH`. `decant` is compiled in
and reconstructs reflowable EPUB 3 from a text-layer PDF, targeting the CrossPoint panel
specifically.

A scanned PDF with no text layer fails with a clear error pointing at OCR, rather than producing
a technically valid EPUB of page images that turns out to be unreadable on the device.

Images are downscaled to the panel, converted to greyscale, and recompressed. Where the panel
geometry is not known — the X3's has not been read off hardware — the geometry step is skipped
rather than guessed at, since downscaling to the wrong size discards detail the panel could have
shown.

## The terminal interface

Run `shelf` with no arguments. Vim keys, `?` for help, `tab` to cycle screens.

- **Library** — filter with `/`, select with `space` or `v`, sync with `s`. A glyph column shows
  each book's sync state against the active device.
- **Devices** — live status: model, firmware, mode, signal, free heap, uptime.
- **Sync** — plan preview, confirmation, then live progress driven by the device's own frames.

`--no-tui` forces the CLI, and a non-interactive stdout does the same automatically, so scripts
and cron jobs never get a screenful of escape codes. Every operation is available headless.

Cover art renders through the kitty graphics protocol or sixel where the terminal supports it,
falling back to unicode half-blocks everywhere else. Inside tmux or screen it deliberately uses
blocks, since multiplexer passthrough is unreliable. Override with `ui.graphics` in the config.

## Commands

```
shelf scan [--deep] [--covers]          index the library directory
shelf ls [QUERY] [--json] [--sort KEY]  list and search
shelf meta BOOK [--set FIELD=VALUE]     show or edit metadata, in place
shelf import FILE... [--move|--link]    add files to the library
shelf shelf create|ls|rm|show|add       manage shelves
shelf devices [--discover]              find and inspect devices
shelf user add|ls|rm|passwd NAME        manage sync and catalog accounts
shelf serve [--no-opds] [--open-catalog] run progress sync and the OPDS catalog
shelf sync [SHELF] [--dry-run]          send a shelf to a device
shelf push FILE... --to /Books          upload directly
shelf pull PATH... [--out DIR]          download from a device
shelf doctor [--offline]                check config, paths, and connectivity
shelf --no-tui                          force CLI mode
```

Search accepts a small query language: bare words hit the full-text index,
`author:"le guin"` and `tag:queue` filter fields, and `and` / `or` / `not` with
parentheses combine them.

## Building

Requires Go 1.25 or newer (a floor set by `modernc.org/sqlite`). No cgo, so
cross-compiling is trivial.

```sh
go build ./...
go test ./...
```

## A firmware hazard worth knowing

`GET /api/settings` **crashes** an X4 running 1.4.1: no response, then a reboot that leaves the SD
card unmounted until it is reseated. Nothing in shelf calls that endpoint, and `shelf doctor` does
not probe it. See `SettingsEndpointUnsafe` in `internal/device/compat.go`.

## Design notes

Two behaviours are worth knowing about, because they are the point of the tool:

**Path pinning.** Once a book occupies a path on the device, that path is frozen in the
manifest. Changing the naming template, the series numbering, or any other metadata produces
zero moves. Every rename, move, and re-upload clears the firmware's `.crosspoint` cache for
that book, which destroys the reading position, so repathing happens only under an explicit
`shelf sync --repath` that lists every book about to lose its place.

**The index is disposable.** `index.db` is a cache derived from the files on disk. Delete it
and rescan; the only thing that cannot be reconstructed is your shelf definitions, which is
why those live in `shelves.toml` and are written there first.

## Design

See [`spec.md`](spec.md) for the full design, the CrossPoint compatibility contract, and the
rationale behind the sync model.

## License

MIT
