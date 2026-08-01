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

M0–M3 are implemented, tested, and verified against real hardware (an X4 on firmware 1.4.1).

| Milestone | Scope | State |
|---|---|---|
| M0 | EPUB parsing, library index, scan and search | done |
| M1 | Device HTTP client and discovery | done |
| M2 | WebSocket upload and the sync engine | done |
| M3 | Terminal interface: library, devices, sync | done |

PDF conversion, EPUB optimization, and KOReader progress sync are designed in `spec.md` but not
yet built. The spec's Settings screen is blocked upstream — see the firmware note below.

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
