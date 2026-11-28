# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Primarily the author: one person with a CrossPoint e-reader (Xteink X4), an EPUB library on disk, and a home server running Outpost. Published for others like them: technical CrossPoint owners who keep their own files, self-host, and are at home in a terminal. Multi-user use is a single household at most (shared progress-sync accounts), not a hosted service.

## Product Purpose

shelf manages an e-book library on disk and gets it onto a CrossPoint e-reader without losing anyone's place. It indexes and edits the library, groups books into shelves, syncs a shelf to the reader over Wi-Fi or an SD card, records reading positions through a built-in KOReader sync server, publishes the library as an OPDS catalog, and turns newsletters into books. Success is a reader that always has the right books on it, where no book ever silently loses its reading position, and a library the person still owns as plain files.

## Positioning

- **The filesystem is the library.** Books are plain files in a directory tree; the SQLite index is a cache that can be deleted at any time. No database owns the files and no sidecars litter them.
- **Path pinning.** CrossPoint clears a book's reading position whenever its path changes, so once a book is on the reader its path is frozen. Metadata edits never move files; repathing happens only behind an explicit CLI flag that lists every position it will cost.
- **Built for this reader.** Sync, OPDS and progress sync are written against the CrossPoint firmware's real behaviour (its parser limits, one-upload-at-a-time, its document-id quirk), not against the generic specs.
- **One binary, four interfaces.** CLI, TUI, web UI and MCP over the same library, following the personal-utils convention.

## Operating Context

- A terminal on a Mac or Linux machine: the TUI for browsing and acting, the CLI for scripts and AI agents (which install `shelf skill`).
- `shelf serve` on a home server, usually supervised by Outpost behind its login gateway and reached over a tailnet. The web UI there is used from a phone or another computer; MCP lets an agent work with the library.
- The reader itself on the same network: it pulls the OPDS catalog, pushes reading progress, and receives syncs. It has no browser and cannot sign in to Outpost.
- Optional Outpost services: the operator's AI models (genre classification, newsletter summaries) and mail (newsletters become EPUBs).

## Capabilities and Constraints

- The CLI, TUI, web UI and MCP are **equal peers**: each is a first-class way in, chosen by situation, and a capability added to one is added to all four or the gap is written down. The web UI is a client of the public `/v1` API with no private endpoints.
- Formats: EPUB, PDF (converted to EPUB on the way to the reader), TXT, XTC.
- The reader is an ESP32-C3 on Wi-Fi: slow, one upload at a time, no resume. Syncs from the server run as jobs one reader at a time; a sync takes minutes.
- Destructive actions (pruning books off the reader, deleting books) always preview and confirm; nothing remote ever repaths.
- AI and mail are enhancements, never dependencies: shelf works fully without them.
- Single static Go binary, no cgo; the web UI is built with Svelte and embedded. Dependencies are kept deliberately small.

## Brand Commitments

- The name is always lowercase: **shelf**, including in titles, headers and the start of a sentence.
- shelf belongs to the author's personal-utils family alongside sous, Tasking and Outpost. It should read as a sibling of those tools, not as a standalone brand.
- Visual standing preference (user-pinned): minimal and clean in the manner of the Apple Store, held to Linear's craft level, with near-black as the only accent and book covers as the colour. The earlier warm, bookish look is retired.
- Voice: plain, specific and honest about consequences. Copy names what an action does to the reader and to reading positions; it does not hype or hedge.

## Evidence on Hand

- README.md and spec.md (design document and CrossPoint compatibility contract).
- Captured firmware responses in `internal/device/testdata/`.
- No users, testimonials, benchmarks or download numbers exist; do not invent any.

## Product Principles

1. **Never lose someone's place.** Reading positions outrank tidiness; any action that could cost one says so, by book, before it runs.
2. **Files first, everything else derived.** The library on disk is the truth; indexes, caches and UI state can be rebuilt.
3. **Every interface is whole.** A person at a terminal, on a phone, or an agent over MCP can do the same things.
4. **Honest about the device.** Design for the reader as it actually behaves: slow, sometimes asleep, one file at a time.
5. **Optional means optional.** Enhancements degrade silently to the plain tool.

## Accessibility & Inclusion

The web UI meets WCAG 2.2 AA: text and placeholder contrast, 3:1 for control boundaries, full keyboard operation with visible focus, labelled fields with errors tied to them, polite live regions for long-running work, and 44px touch targets on touch devices. Light and dark themes both meet the standard.
