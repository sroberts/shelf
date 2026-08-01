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

M0–M2 are implemented and tested. Not yet verified against real hardware.

| Milestone | Scope | State |
|---|---|---|
| M0 | EPUB parsing, library index, scan and search | done |
| M1 | Device HTTP client and discovery | done |
| M2 | WebSocket upload and the sync engine | done |

The TUI, PDF conversion, EPUB optimization, and KOReader progress sync are designed in `spec.md`
but deliberately out of scope for now.

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
