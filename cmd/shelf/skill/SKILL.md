---
name: shelf
description: Manage an EPUB library on disk and sync it to a CrossPoint (Xteink X3/X4) e-reader with the `shelf` CLI. Use when the user wants to find, add, edit, shelve, delete or sync books, check what they are reading, or see library stats, genres or imported newsletters.
---

# Driving shelf from the command line

`shelf` is one binary. Every command below is non-interactive when given its
flags, and every listing takes `--json` (newline-delimited, one object per
line) so output pipes into `jq`. Prefer `--json` whenever you will read the
output rather than show it.

Never run bare `shelf` from an agent: with no command it opens the terminal UI.

## Referring to books

A `BOOK` argument is one of:

- `id:42` — the `id` field from `shelf ls --json`. Unambiguous, so use it.
  Ids change whenever the index is rebuilt; look them up fresh, do not store them.
- a path to the file;
- words that match exactly one title. Several matches is an error that lists them.

## Finding books

```sh
shelf ls --json                         # everything
shelf ls --json earthsea                # full-text words
shelf ls --json 'author:"le guin" and not tag:read'
shelf ls --json --sort added-desc --limit 10
shelf ls --json --genre fantasy         # AI genre (see shelf genres)
shelf meta id:42 --json                 # one book in full
```

Query syntax: bare words hit the full-text index; `title:`, `author:`,
`series:`, `publisher:`, `path:` match substrings; `tag:`, `format:`,
`language:` match exactly; combine with `and`, `or`, `not` and parentheses;
quote values with spaces. Adjacent terms are ANDed.

## Changing the library

```sh
shelf import ~/Downloads/book.epub      # copies in, names it by the template, indexes it
shelf import --move --dry-run ~/Inbox/* # show destinations first
shelf meta id:42 --set title="The Farthest Shore" --set series=Earthsea --set series_index=3
shelf meta id:42 --set tags=fantasy,queue     # replaces the tag list; tags= clears it
shelf rm --dry-run id:42                # then: shelf rm --yes id:42 (deletes the file)
```

Metadata editing works on EPUBs only and rewrites the file in place. Always
`--dry-run` a delete and confirm with the user before `rm --yes`.

## Shelves

A shelf is a saved query (`--query`) or a hand-picked set (`--manual`).

```sh
shelf shelf ls --json
shelf shelf create queue --manual
shelf shelf create unread --query 'not tag:read'
shelf shelf add queue id:42 id:43
shelf shelf remove queue id:43
shelf shelf show queue --json
shelf shelf rm queue                    # deletes the shelf, not its books
```

## Syncing to the e-reader

```sh
shelf devices --json                    # configured readers and whether they answer
shelf devices add reader --host 192.168.1.42
shelf sync queue --dry-run              # ALWAYS preview first and show the user
shelf sync queue --yes                  # then send it
shelf sync --dry-run --prune            # whole library; --prune deletes device files not in it
```

- Preview every sync with `--dry-run` and show the plan before running it.
- **Never pass `--repath`** unless the user asks for it by name after seeing
  what it does: it moves books on the device and destroys their reading
  positions. Nothing else ever moves a book already on the device.
- `--prune` deletes files from the reader. Confirm with the user first.
- The reader must be awake in File Transfer mode; an unreachable device is an
  error, not a reason to retry in a loop.

## Reading progress

The reader pushes positions to `shelf serve` (KOReader sync).

```sh
shelf progress --json                   # most recently read first
shelf progress --user alice --json
shelf progress clear id:42 --yes        # forgets shelf's copy only
```

Clearing does not move the reader: it keeps its own position and pushes it
again next time the book is open.

## Reader accounts

The reader signs in to the OPDS catalog and progress sync with a reader account.

```sh
shelf user ls --json
shelf user add --generate x4                                  # prints a typeable password, once
shelf user add --generate --url https://shelf.example.ts.net x4   # setup for a reader using Funnel
shelf user passwd --generate x4                               # new password; positions are kept
```

The password is printed once and stored only as a hash: pass it on to the user
right away. `shelf user rm` deletes the account's reading positions, so ask first.

## Everything else

```sh
shelf stats --json        # books, authors, series, tags, shelves, size, formats, genres
shelf genres --json       # AI genres with counts; empty unless shelf serve runs under Outpost with AI
shelf mail --json         # newsletter import state (shelf serve under Outpost with mail)
shelf tags --json
shelf doctor --offline    # config, paths and index health
```

Exit status: 0 success, 1 failure, 2 usage error (bad flags or arguments).
