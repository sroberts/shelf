---
version: 1
slug: "web-src-routes-library-svelte"
primary_target: "web/src/routes/Library.svelte"
related_targets: []
---

# Surface brief: Library

Scope: the web UI's Library page (`/`), the cover grid with search, filters, sort and upload. Visitor mode: Operate.

Audience and job: the owner, or a household member, on a laptop or phone, finds a book, sees how far they've read, or drops new files in. They visit often; it must be fast to scan and to filter.

Constraints: covers of real books, many without art (generated placeholder covers); upload by drag-and-drop; filters persist in the URL; WCAG 2.2 AA in light and dark.

## Direction contract

THESIS: The library as a showroom: every book on its own neutral tile, cover lifted, nothing else coloured. Refuses the default of a dense admin table, and the warm paper bookshelf.

OWN-WORLD: Near-white ground, soft neutral tiles, system sans at a few weights, near-black as the only accent, hairlines rare, covers the only saturated colour; dark mode true near-black with tiles one step lighter.

STORY: The visitor sees their books first, finds one by typing or a filter, reads at a glance what's in progress, and drops files on the page to add them.

FIRST VIEWPORT: Slim top bar (shelf wordmark lowercase, Library/Shelves/Reading/Readers, search field right). Below it a large semibold page title with the book count, one row of segmented filters and sort aligned right, then the grid: large tiles, generous gutters, cover centred and lifted on each tile, title and author below in Body and Meta. In-progress books show a thin near-black progress bar under the cover. Primary action (Add books) is the one near-black button beside the title.

FORM: Pinned by the user (Apple Store minimalism at Linear's craft level, near-black only accent); first on the list by instruction, not by roll. seed: none (pinned by user)

Signature move: the cover tile, a book lifted off a soft neutral plinth like a product in a store, used everywhere a book appears.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Unresolved

- Exact tokens (greys, radii, type sizes) are resolved during implementation, then DESIGN.md is re-documented from the build.
- Placeholder cover treatment in the new world (currently hue-from-title; must become neutral-friendly without losing distinguishability).
