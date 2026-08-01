# Device fixtures

Captured from real hardware, not hand-written. When the firmware and these files
disagree, re-capture rather than editing by hand.

**Two fields are anonymized.** The `ip` in `status.json` and the filenames in
`files_root.json` were replaced before publishing: one is a home LAN address,
the other was somebody's actual reading list. Everything the tests assert on --
the field names, the sizes, the flags, the absence of any mtime or checksum --
is exactly as the device returned it. Anonymize the same two fields when
re-capturing.

| File | Source |
|---|---|
| `status.json` | `GET /api/status` |
| `files_root.json` | `GET /api/files?path=/` |
| `files_subdir.json` | `GET /api/files?path=/shelf-test` (a directory shelf created) |
| `discovery.txt` | UDP `hello` to port 8134 |

## Device used

- Model **X4**, firmware **1.4.1**
- Reported over a weak link (`rssi` around -90 dBm) with roughly 87 KB free heap
- Hostname `CrossPoint-Reader-000000000000`, resolving as `crosspoint.local`

## What the capture confirmed

- The UDP discovery reply matches the documented format exactly:
  `crosspoint (on <hostname>);81`
- `/api/files` returns `name`, `size`, `isDirectory`, `isEpub` and **no mtime or
  checksum**, so change detection cannot depend on either. Confirmed on hardware.
- Dotfiles are absent from the listing: `/.crosspoint` does not appear at the
  root even though it exists on the card. This is why shelf keeps its own state
  in a visible `/shelf` directory.
- The firmware version is `1.4.1`, not the `1.0.0` the draft spec assumed.
- A WebSocket upload to port 81 succeeds end to end, including paths with CJK
  characters, and a pulled file is byte-identical to the local source.

## Undocumented behaviour, observed on hardware

- **A missing directory returns `200` with `[]`, not `404`.** Do not use a
  not-found error to detect a missing directory; stat the parent instead.
- `POST /mkdir` answers `Missing folder name` when `name` is absent, confirming
  that `name` is required and `path` alone is not enough.
- `POST /mkdir` answers `Failed to create folder` when the SD card is not
  mounted -- while `/api/status` still reports the device as healthy. A `mkdir`
  that fails for every path points at the card, not the path.
- `POST /delete` answers `All items deleted successfully`, and does accept an
  empty directory.
- `GET /download` answers `Item not found` for a missing file.

## Not captured, deliberately

`GET /api/settings` **crashed the device** on 1.4.1 — no response, then a reboot
out of File Transfer mode, with an empty panic reason in the crash report. See
the `SettingsEndpointUnsafe` note in `../compat.go`. Do not re-run it casually.
