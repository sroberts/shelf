# Contributing

Welcome. This guide is the canonical source for how PRs get reviewed and merged in this
repository — both for human contributors and for AI coding agents.

Read [`CLAUDE.md`](CLAUDE.md) first. It covers the architecture, the three invariants, and the
firmware quirks that force most of the design. This file covers process; that one covers the code,
and the two deliberately do not repeat each other.

## Workflow

1. Branch from main: `git checkout -b <type>/<short-name>`.
2. Make focused commits — one logical change per PR.
3. Run the full test suite locally before opening the PR.
4. Open a PR; the template lists the per-merge checks.

Before pushing, run what CI runs:

```sh
gofmt -l .          # must print nothing
go vet ./...
go test -race ./...
```

CI also cross-compiles for linux and darwin on amd64 and arm64. The binary is cgo-free and must
stay that way — a dependency needing cgo breaks static builds and cross-compilation, which is why
the SQLite driver is `modernc.org/sqlite`.

## What gets a PR rejected

Most of these come from bugs that have actually shipped here.

**Breaking path pinning.** Any change that lets an already-pinned book take a new device path
outside an explicit `--repath`. Every rename, move, and re-upload clears the firmware cache holding
that book's reading position, so this destroys user data silently and no test failure will look
like data loss. `internal/sync/plan.go` takes pinned destinations from the manifest, never from the
current naming template.

**Planning a directory deletion.** `--prune` removes files only. Treating a directory as an orphan
once came close to deleting the sync root.

**Making `plan.go` impure.** No I/O, no clock, no randomness in the planner. Purity is what makes
the decision table testable and guarantees `--dry-run` describes exactly what a real run does. If
the planner needs something, add it to `Input`.

**Weakening a test to make code pass.** When a test fails, either the code is wrong or the test
encodes a claim that is wrong. Both are fine outcomes — but changing an assertion has to be the
point of the PR, argued in the description, not a quiet edit buried in an unrelated change.

**Blocking I/O in a Bubble Tea `Update`, or writing to stdout from the TUI.** The first stalls the
display behind a slow Wi-Fi link; the second corrupts the frame. Return a `tea.Cmd`, and log to
`$XDG_STATE_HOME/shelf/shelf.log`.

**Hand-editing generated or captured files.** Golden frames come from
`go test ./internal/tui/ -update-golden`. Device fixtures in `internal/device/testdata/` are real
hardware captures — re-capture rather than edit, and anonymize the `ip` field and any filenames,
which otherwise publish a home network address and somebody's reading list.

**New dependencies without discussion.** Each one is a future packaging problem. Open an issue
first; the bar is high and the list is deliberately short.

**Assuming API behavior the firmware does not have.** No mtime, no checksum, no upload resume, and
a missing directory returns `200 []` rather than `404`. Tests assert these gaps stay gaps. If a
future firmware fills one in, that is a feature PR with its own discussion, not an incidental
change.

**Unrelated formatting churn.** It hides the real change in review.

## Testing expectations

New behavior needs a test that would fail without it. Beyond that:

- Logic that can be pure should be pure and table-driven. `internal/sync/plan_test.go` is the model.
- Device behavior is tested against the fakes in `internal/device`: an `httptest` server and a
  WebSocket peer replaying every documented `ERROR:` string. Add new firmware error strings there
  and in `device/errors.go`, rather than matching text at call sites.
- Hardware tests live behind the `hardware` build tag and must skip cleanly with no device present.
- Anything touching filenames needs a non-ASCII case. NFC normalization and display-width
  alignment are both real bugs that only surface with accented or CJK text.

## Style

- Format with the language's standard tool (gofmt, prettier, etc.).
- Don't reformat unrelated lines; keep diffs minimal.
- Comments explain WHY, not WHAT — especially where code looks strange because the firmware is
  strange. One comment naming the device behavior that forced an odd branch is worth more than
  three restating the syntax.

A `.gitmessage` commit template is included; opt in per clone with
`git config commit.template .gitmessage`.

## Reporting device bugs

Firmware misbehavior, as opposed to a shelf bug, belongs upstream at
[crosspoint-reader/crosspoint-reader](https://github.com/crosspoint-reader/crosspoint-reader).
Search first — several web-endpoint stability issues are already open. Include the firmware version
from `GET /api/status`, and scrub your LAN address and book filenames before posting.

## Asking for help

Open a draft PR or an issue. Don't sit on a stuck branch.
