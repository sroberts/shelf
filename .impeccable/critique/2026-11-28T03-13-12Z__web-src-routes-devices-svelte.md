---
target: Reader page (Add a reader)
total_score: 21
max_score: 40
na_heuristics: 
p0_count: 1
p1_count: 2
target_identity: "file:/Users/sroberts/Developer/shelf/web/src/routes/Devices.svelte"
target_fingerprint: "sha256:2fbc147d4fa3d0b88601bdd31e9765f08824de222d65d9dc4443213a91220b12"
target_path: /Users/sroberts/Developer/shelf/web/src/routes/Devices.svelte
timestamp: 2026-11-28T03-13-12Z
slug: web-src-routes-devices-svelte
---
Method: dual-agent (A: design review · B: detector + browser)

| # | Heuristic | Score | Key issue |
|---|---|---|---|
| 1 | Visibility of status | 2 | SD reader shown as Wi-Fi; "Never synced" ignores previews; Recent syncs never refreshes |
| 2 | Match real world | 2 | Raw MKDIR/UPLOAD verbs; "12 planned changes" for 6 books; error says "host", field says "Address" |
| 3 | User control | 3 | Cancel keeps stale rejected values |
| 4 | Consistency | 2 | Two primary Sync buttons on one card; Address field taller/misaligned |
| 5 | Error prevention | 1 | Prune sync stays green primary, no confirm; host format checked only server-side |
| 6 | Recognition | 3 | Address help lives above the grid, not under the field |
| 7 | Flexibility | 3 | Fine for one device |
| 8 | Minimalist | 2 | Six controls per card; MKDIR noise |
| 9 | Error recovery | 1 | Host error is a detached bottom toast, "Bad request:", no aria-invalid |
| 10 | Help | 2 | No "can't find the address?" help |
| Total | | 21/40 | Acceptable |

Priority issues:
- [P0] SD-card reader labelled Wi-Fi: Devices.svelte:170,174 compare transport to 'sdcard', API sends 'sd'.
- [P1] Add a reader: Address input 53px vs 40px and 12px lower (no <small>, grid stretch, base.css:152 / Devices.svelte:303); help for Address sits above grid; required/optional unmarked; errors as toast; form opens above list, no focus move; Cancel keeps values; input borders 1.84:1 / 1.72:1; dark placeholder 3.5:1.
- [P1] Prune sync has no guard: stays btn primary, no confirm listing books that lose positions; notice flex gap mangles spacing.
- [P2] Duplicate Sync buttons, MKDIR-first ops list, size glued to path, ops-not-books count.
- [P2] Stale Recent syncs / Never synced; aria-live wraps whole job block (chatty).

Detector: CLI 0 findings; overlay: cream-palette (intentional, false positive), 3 dark placeholder low-contrast (real).
