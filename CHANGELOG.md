# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- `shelf rm` and `shelf delete` commands to remove books from both the index and filesystem with interactive confirmation, `--yes` / `-y`, `--dry-run`, and `--quiet` flags.
- `DeleteBook` and `DeleteBookByID` methods in `internal/library` for deleting books from the database and removing their files from disk.
- `PruneEmptyDirs` helper in `internal/library` to clean up empty parent directories up to the library root upon book deletion.
- Books can be referred to by index id as `id:42` in `shelf rm`, `shelf delete`, `shelf meta`, `shelf shelf add` and `shelf shelf remove`. `shelf meta` shows the id, and JSON output from `shelf ls`, `shelf meta` and `shelf shelf show` includes it as `id`.

- The TUI can add a network device: press `n` on the Devices screen, prefilled from a discovered device under the cursor. The device is appended to `config.toml` as a `[[device]]` block, leaving existing content and comments untouched, and is usable without restarting.

- `shelf serve` conforms to the Outpost service spec (v0.9.4): `GET /up` healthcheck, `--port` and `OUTPOST_PORT` binding loopback only, all state under `OUTPOST_STORAGE_DIR`, JSON log lines with levels, and a shutdown held under Outpost's ten-second SIGKILL deadline. Behind Outpost's login gateway the OPDS catalog accepts a verified `X-Outpost-Identity` token in place of a password.
- `shelf serve --scan` re-indexes the library in the background at startup.
- `shelf serve` offers a JSON REST API under `/v1` (described by an OpenAPI 3.1 document at `/v1/openapi.json`) and an MCP server at `/mcp` (Streamable HTTP), both authenticated with a shelf account or an Outpost identity. They search, browse and download books, manage manual shelves, and report stats and reading progress.
- Under Outpost, `shelf serve` uses the operator's System 1 model to classify each book's genre once, keeping the answers in `ai.db`. It also imports mail sent to its Outpost addresses as EPUB books under `Mail/`, with an LLM-written description when a model is available. `--no-ai` and `--no-mail` turn these off.
- A web UI at `/` on `shelf serve` (Svelte, embedded, installable as a PWA). It can browse and search the library, upload books, edit EPUB metadata, create, fill and delete shelves, see and clear reading progress, and add readers, then preview, run and watch syncs to them. It is a client of `/v1` with no private endpoints.
- New API routes, each with a matching MCP tool:
  - `GET /v1/me`.
  - `POST /v1/books` (upload).
  - `PATCH /v1/books/{id}` (metadata).
  - `GET /v1/books/{id}/cover`.
  - `POST` and `DELETE /v1/shelves`.
  - `DELETE /v1/progress/{id}`.
  - `GET` and `POST /v1/devices`.
  - `POST /v1/devices/{name}/sync`, with jobs under `/v1/jobs`.
- Syncs started from the API run as one job per device and can be cancelled. They never repath. An MCP `sync_to_device` call only plans unless `dry_run` is false.
- CLI: `shelf genres`, `shelf stats`, `shelf progress` / `progress clear`, `shelf mail`, `shelf devices add`, `shelf ls --genre`, genre in `meta` and `--json`, and `shelf skill show|install`, which installs an agent skill describing the CLI.
- TUI: edit metadata (`e`), add to or remove from a shelf (`+`/`-`), clear a reading position (`x`), delete (`d`), and import (`I`) from the library screen. The detail panel shows genre, and which device reported the reading position and when.
- Reader accounts can be created from the web UI (**Readers → Reader accounts**), the API (`/v1/accounts`) and MCP (`create_reader_account` and friends), as well as the CLI. Each can generate a password that is easy to type on the reader and strong enough for Tailscale Funnel, shown once with the reader's full setup. `shelf user add` and `shelf user passwd` take `--generate` and `--url`.
- Failed sign-ins are limited per client address (10 in 15 minutes) across the catalog, progress sync and the API, since the reader's paths may be on the internet through Funnel. Behind a local proxy (Outpost's gateway or Funnel), the address is read from `X-Forwarded-For`, and that header is trusted only from loopback.
- A metadata edit carries a book's AI genre over to its new hash, so it is not classified (and paid for) twice.
- `outpost.toml` descriptor, attached to each release with a `SHA256SUMS` file, so `outpost add` can install and verify shelf.

### Fixed
- Syncing to a reader on firmware 1.6.1 no longer fails with "Directory listing failed" when the destination folder does not exist yet. That firmware answers a missing folder with a 500 rather than an empty list; shelf now checks the parent folder, treats a genuinely missing folder as empty and creates it, and still reports a folder that exists but cannot be read.

### Changed
- `shelf serve` answers `/` with the web UI instead of kosync's plain-text help page. Progress sync's own paths (`/users/*`, `/syncs/*`) are unchanged.
- State-changing requests to `shelf serve` whose `Origin` or `Referer` names another host are refused with 403. The reader sends neither header and is unaffected.
- The TUI is built on Bubble Tea v2, Lip Gloss v2 and Bubbles v2. Bubble Tea v1 queried the terminal's background colour at package init, so every CLI command, `shelf version` included, sent an escape sequence to the terminal and waited up to five seconds for a reply some terminals never send. The query now runs only when the TUI starts.

## [0.1.0] - 2026-09-14

### Added
- Comprehensive test coverage for `internal/target` package.
- End-to-end integration test suite in `cmd/shelf/e2e_test.go` verifying CLI workflows and command execution with isolated test environments.
- Static analysis configuration with `.golangci.yml`.
- Multi-stage CI pipeline with concurrency cancellation, `golangci-lint`, `govulncheck` security audit, and code coverage artifact tracking.
- Automated multi-platform CD release workflow on tag push in `.github/workflows/release.yml`.
- Dependabot configuration for automated weekly Go module and GitHub Action updates.

### Changed
- Streamlined `README.md` to focus on user-facing features, installation, and commands, removing internal development milestones and firmware trivia.

### Security
- Upgraded `golang.org/x/image` to v0.46.0 to resolve vulnerability GO-2026-6222.
