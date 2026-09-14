# shelf

A fast terminal library manager for [CrossPoint](https://github.com/crosspoint-reader/crosspoint-reader) e-readers (Xteink X3 / X4).

Your filesystem is the library. The device is a path-stable sync target. The SQLite index is a derived cache you can delete at any time.

## Why

- **Filesystem-first:** Books live in a plain directory tree you can browse with standard tools like `ls`. No proprietary database locks up your files, and no `metadata.opf` sidecars clutter your folders.
- **Path stability:** CrossPoint clears its render and progress cache whenever a book is moved or renamed. `shelf` pins device paths permanently so metadata edits never reset your reading position.
- **No bloat:** Single static binary. No GUI dependencies, no Qt, no Python runtime, and no cgo.

## Features

- **Terminal UI & headless CLI:** Interactive TUI with cover art support (Kitty, Sixel, and unicode half-blocks) plus a headless mode (`--no-tui`) for scripting and automation.
- **Flexible sync transports:** Sync wirelessly over Wi-Fi (HTTP/WebSocket) or directly to a mounted microSD card for fast initial transfers.
- **Reading progress sync:** Built-in KOReader sync server to sync reading progress with CrossPoint devices.
- **OPDS catalog:** Built-in OPDS server to browse and download books directly from your reader or any OPDS client.
- **In-process PDF conversion:** Converts text-layer PDFs to reflowable EPUB 3 targeted at e-ink panels without needing Calibre or external tools installed.
- **Metadata editing & search:** Edit EPUB metadata in place and search using full-text queries and field filters.

## Installation

Requires Go 1.25 or later.

```sh
# Install directly
go install github.com/sroberts/shelf/cmd/shelf@latest

# Or build from source
go build -o shelf ./cmd/shelf
```

## Quick Start

1. **Scan your library:**
   ```sh
   shelf scan ~/Books
   ```

2. **Launch the TUI:**
   ```sh
   shelf
   ```
   - Navigate with arrow keys or Vim bindings (`j` / `k`)
   - `/` to search and filter
   - `Space` or `v` to select books
   - `s` to sync selected books to device
   - `i` to toggle the book detail panel
   - `Tab` to cycle views (Library, Devices, Sync)
   - `?` for help, `q` to quit

3. **Or run via CLI:**
   ```sh
   shelf ls author:"Le Guin"
   shelf sync --dry-run
   ```

## Syncing

### Over Wi-Fi

Discover and inspect devices on your local network:

```sh
shelf devices --discover
shelf sync
```

Use `shelf sync --dry-run` to preview planned transfers and disk usage before uploading.

### Direct via microSD Card

Syncing directly to the microSD card is much faster for large libraries or first-time syncs. Insert the card into your computer and add a device entry to your configuration file (`~/.config/shelf/config.toml`):

```toml
[[device]]
nickname = "card"
transport = "sd"
mount = "/Volumes/CROSSPOINT"
root = "/Books"
```

Sync as usual with `shelf sync`. Always safely eject the card before reinserting it into the reader.

## Reading Progress & OPDS Server

`shelf serve` runs both the KOReader reading-progress sync service and an OPDS catalog server.

```sh
# Create a user account
shelf user add <username>

# Start the server
shelf serve
```

- **Reading Progress:** In CrossPoint's sync settings, configure KOReader sync with the server address (`http://<this-machine>:8080`) and your user credentials. Set `[kosync] user` in your config to see reading progress in `shelf ls`.
- **OPDS Catalog:** Add `http://<this-machine>:8080/opds` in CrossPoint's OPDS browser settings. *(Note: CrossPoint's OPDS client only downloads EPUB files; other formats are filtered out by the device).*
- Additional server flags: `--open-catalog` disables OPDS password authentication, and `--no-opds` disables the catalog endpoint.

## Commands

```
shelf scan [--deep] [--covers]          Index the library directory
shelf ls [QUERY] [--json] [--sort KEY]  List and search books
shelf tags [--json]                     List tags with book counts
shelf meta BOOK [--set FIELD=VALUE]     Show or edit book metadata in place
shelf import FILE... [--move|--link]    Add files to the library
shelf convert FILE... [--out DIR]       Convert PDF or TXT to EPUB
shelf optimize FILE... --profile NAME   Rebuild an EPUB for a device panel
shelf shelf create|ls|rm|show|add       Manage named shelves
shelf devices [--discover]              Find and inspect devices
shelf user add|ls|rm|passwd NAME        Manage sync and catalog accounts
shelf serve [--no-opds] [--open-catalog] Run progress sync and OPDS catalog
shelf sync [SHELF] [--dry-run]          Send a shelf to a device
shelf push FILE... --to /Books          Upload directly to a device path
shelf pull PATH... [--out DIR]          Download files from a device
shelf doctor [--offline]                Check configuration and index health
shelf version [--full]                  Print version information
shelf --no-tui                          Force CLI mode (headless)
```

### Search Syntax

Search queries support bare words and field qualifiers:
- Full-text search: `earthsea`
- Field filters: `author:"le guin"`, `series:"earthsea"`, `tag:sci-fi`
- Boolean logic: `author:"dick" and not tag:read`

## Documentation

- [`spec.md`](spec.md) — Technical specification and CrossPoint compatibility details.
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — Development workflow, architecture invariants, and testing guidelines.

## License

MIT
