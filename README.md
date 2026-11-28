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

To save a device, add a `[[device]]` block to `~/.config/shelf/config.toml`, or press `n` on the TUI's Devices screen (`2`) to add one there. With a discovered device under the cursor, the form is filled in for you.

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
- Additional server flags: `--open-catalog` disables OPDS password authentication, `--no-opds` disables the catalog endpoint, `--scan` re-indexes the library in the background at startup, and `--port N` listens on `127.0.0.1:N` only (for running behind a proxy; use `--listen` for the LAN).
- `GET /up` is an unauthenticated healthcheck that answers `200 ok` while the server is running.

### Web UI

`shelf serve` also serves a web UI at `/`. Open the server's address in a browser and sign in with a shelf account; under Outpost's login gateway you're already signed in. From it you can:

- browse and search the library by author, series, tag, format and genre, and download books;
- drag files onto the page to add them to the library (EPUB, PDF, TXT and XTC, up to 512 MiB per upload), placed by the naming template just like `shelf import`;
- edit an EPUB's title, authors, series, tags and description. The change is written into the file, which stays where it is, so the book keeps its place on the reader;
- create and delete shelves, either saved searches or hand-picked, and add or remove books on hand-picked ones;
- see what you're reading: how far each book has got, on which device, and when. You can also clear a stale position;
- add a Wi-Fi reader, preview a sync, run it and watch its progress. Books already on the reader never move, and moving them (`--repath`) stays CLI-only, because it costs their reading positions.

It installs as an app on a phone (it's a PWA), and is a client of the same `/v1` API as everything else, with no private endpoints.

The UI is built with Svelte and Vite and embedded in the binary. Release builds include it. When building from source, build the UI first, or the binary serves a page saying it isn't built (the API, MCP and catalog work either way):

```sh
cd web && npm ci && npm run build && cd ..
go build -o shelf ./cmd/shelf
```

### API and MCP

The same server offers a JSON REST API under `/v1` and an MCP server at `/mcp`. Both take the same account as the catalog: HTTP Basic with a shelf account, or a signed-in Outpost user (see below). They're authenticated even with `--open-catalog`, because they can change the library. An account the reader registered itself, through kosync's open `/users/create`, is for progress sync and the catalog only; make one with `shelf user add` (or from the web UI) to sign in to the API, MCP and the web UI.

- **REST:** search books (`GET /v1/books?q=author:"le guin"`); fetch, download, or get the cover of one (`/v1/books/{id}`, `/file`, `/cover`); upload books (`POST /v1/books`) and edit their metadata (`PATCH /v1/books/{id}`); browse authors, series, tags and genres; create, delete and fill shelves; read and clear reading progress (`/v1/progress`); list and add readers (`/v1/devices`) and sync to them as jobs you poll (`POST /v1/devices/{name}/sync`, `/v1/jobs/{id}`); library stats; and `GET /v1/me`, which says who you are and which features this server has on. The full description is at `GET /v1/openapi.json`, which needs no login.
- **MCP:** the Streamable HTTP transport at `/mcp`, with tools to search, read, and edit books (`search_books`, `get_book`, `update_book_metadata`, `list_values`), manage shelves (`list_shelves`, `get_shelf`, `create_shelf`, `delete_shelf`, `add_to_shelf`, `remove_from_shelf`), and check reading progress (`reading_progress`, `clear_reading_progress`). Further tools sync (`list_devices`, `add_device`, `sync_to_device`, `get_sync_job`, `cancel_sync_job`) and give `library_stats`. `sync_to_device` only plans unless it's called with `dry_run: false`, so an agent shows you the plan before anything is sent.

Shelf edits made through either one are written to `shelves.toml`. If that write fails, the edit is undone.

### Running under Outpost

shelf follows the [Outpost service spec](https://github.com/sroberts/outpost/blob/v0.9.4/outpost-service-spec.md) (revision v0.9.4), and ships an `outpost.toml`, so it can be installed with:

```sh
outpost add https://github.com/sroberts/shelf
```

When `OUTPOST_STORAGE_DIR` is set, `shelf serve`:

- listens on `127.0.0.1:$OUTPOST_PORT` (or `--port`), and refuses to start without a port rather than falling back to binding every interface;
- keeps all of its state, including the config file, index, `shelves.toml`, `progress.db`, and the default library (`$OUTPOST_STORAGE_DIR/library`), inside the storage directory, so `outpost backup` and `outpost restore` cover everything. `--library` or `library_root` in `$OUTPOST_STORAGE_DIR/config/config.toml` can point it elsewhere, but a library outside storage is not backed up;
- logs one JSON object per line to stdout, with `level` and `msg`, and reports a fatal error as a JSON line on stderr before exiting non-zero;
- exits within ten seconds of SIGTERM.

If Outpost gives shelf its models and mail, `shelf serve` also uses them. Both are optional: without them, or with `--no-ai` / `--no-mail`, it serves exactly as before.

- **AI models (`OUTPOST_AI_URL`):** each book is classified into a genre once, by the System 1 model, in the background. Genres show up in the API and MCP, and you can filter by them. Answers are kept in `data/ai.db` inside storage and are never requested twice, not even after the index is rebuilt. When the LLM is available it also writes a short description for each newsletter imported from mail. If a model is unavailable or not assigned to shelf, shelf carries on without it.
- **Mail (`OUTPOST_MAIL_URL`):** mail sent to shelf's addresses (for example `you+shelf@gmail.com`, or an alias you make for one newsletter) becomes an EPUB in the library under `Mail/<sender>/`, titled with the date and subject, and is then deleted from Outpost's mailbox. That way newsletters reach the reader through the catalog or `shelf sync`. Outpost keeps no attachments, so only the text comes through. A message delivered twice is imported once.

To manage accounts or import books, run the CLI against the same storage directory:

```sh
OUTPOST_STORAGE_DIR=~/.outpost/data/shelf shelf user add reader
OUTPOST_STORAGE_DIR=~/.outpost/data/shelf shelf import ~/Downloads/*.epub
```

**With the login gateway on** (`OUTPOST_AUTH=gateway`), a browser that has signed in to Outpost can open the catalog without a second password. shelf verifies the signed `X-Outpost-Identity` token against `OUTPOST_JWKS_URL`, and never trusts the plain `X-Outpost-User` header. The reader can't sign in to the gateway, so `outpost.toml` suggests the paths it uses (`/opds`, `/opds/*`, `/users/auth`, `/syncs/progress`, `/syncs/progress/*`) as `public_paths`. shelf authenticates each of those requests itself with your shelf account. Account creation (`/users/create`) is left behind the gateway, so create accounts from the web UI or with `shelf user add` (see below). The reader also needs a network route to shelf's tailnet address, which an ESP32 can't join by itself.

### Reading away from home (Tailscale Funnel)

The reader can't join a tailnet, so outside your home network it reaches shelf through Tailscale Funnel. Under Outpost, turn on Internet access for shelf. That puts only the reader's paths (`/opds`, `/opds/*`, `/users/auth`, `/syncs/progress`, `/syncs/progress/*`) on the internet, at shelf's own `https://<node>.<tailnet>.ts.net` address. The reader's firmware speaks HTTPS, so the Funnel address works for both the catalog and progress sync.

Give the reader an account made for this. On the **Readers** page of the web UI, **Reader accounts → New account** creates one with a generated password. Then it shows everything to enter on the reader, once: the OPDS server, the sync server, the username and the password. From a terminal:

```sh
shelf user add --generate --url https://shelf.example.ts.net x4
```

Generated passwords are four groups of four lowercase letters and digits, such as `k7mq-2xrf-n4dp-9vht`. That's easy to type on the reader's keyboard, and too strong (80 bits) to guess over the internet. A password you choose yourself must be at least 10 characters when it's set through the web UI or the API. Because these paths are public, shelf refuses an address after 10 wrong passwords in 15 minutes. It counts by address, not by account, so someone guessing can't lock your own reader out. shelf stores only a salted hash, so a lost password is reset, not recovered.

## Commands

```
shelf scan [--deep] [--covers]          Index the library directory
shelf ls [QUERY] [--json] [--sort KEY]  List and search books (--genre G to filter)
shelf tags [--json]                     List tags with book counts
shelf genres [--json]                   List AI-assigned genres with book counts
shelf stats [--json]                    Library totals
shelf meta BOOK [--set FIELD=VALUE]     Show or edit book metadata in place
shelf rm BOOK... [--yes] [--dry-run]    Delete books from the index and disk
shelf import FILE... [--move|--link]    Add files to the library
shelf convert FILE... [--out DIR]       Convert PDF or TXT to EPUB
shelf optimize FILE... --profile NAME   Rebuild an EPUB for a device panel
shelf shelf create|ls|rm|show|add       Manage named shelves
shelf devices [--discover]              Find and inspect devices
shelf devices add NICK --host H         Add a Wi-Fi device to config.toml
shelf progress [--user U] [--json]      Reading positions, most recent first
shelf progress clear BOOK... [--yes]    Forget shelf's copy of a reading position
shelf mail [--json]                     Mail import status
shelf user add|ls|rm|passwd NAME        Manage reader accounts (--generate makes a password)
shelf serve [--port N] [--scan]         Run the web UI, API, MCP, OPDS catalog and progress sync
shelf skill show|install [--dir DIR]    Print or install the agent skill (SKILL.md)
shelf sync [SHELF] [--dry-run]          Send a shelf to a device
shelf push FILE... --to /Books          Upload directly to a device path
shelf pull PATH... [--out DIR]          Download files from a device
shelf doctor [--offline]                Check configuration and index health
shelf version [--full]                  Print version information
shelf --no-tui                          Force CLI mode (headless)
```

`BOOK` is a file path, a search that matches exactly one book (`shelf meta earthsea`), or an
index id written as `id:42`. `shelf meta` and `shelf ls --json` show each book's id. Ids are
renumbered whenever the index is rebuilt, so look one up just before using it. A bare number
is treated as a search term, never as an id.

Clearing a reading position only clears shelf's copy. The reader keeps its own and sends it
again the next time the book is open with sync on.

`shelf skill install` writes `~/.claude/skills/shelf/SKILL.md`, which teaches an agent such as
Claude Code how to drive shelf's CLI. It won't overwrite an edited copy without `--force`.

In the TUI, the book under the cursor takes `e` (edit metadata), `+` / `-` (add to or remove from
a shelf), `x` (clear its reading position), `d` (delete), and `I` imports a file. `?` lists every key.

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
