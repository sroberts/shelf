# Contributing

This is a solo project with a public repo. That shapes the process: there is no review queue and
no second pair of eyes, so the checks that would normally live in review are automated or written
down here instead.

Read [`CLAUDE.md`](CLAUDE.md) first. It covers the architecture, the three invariants, and the
firmware quirks that force most of the design. This file covers process; that one covers the code,
and the two deliberately do not repeat each other.

## How the repo actually works

**A branch per milestone, not per commit.** Work lands on a branch named for the thing being
built — `m3-tui`, `m4-convert-optimize` — which accumulates several commits and merges when the
milestone is coherent. Branches are pushed as they go, so the work is visible before it lands.

**Linear history, no merge commits.** Milestone branches fast-forward into `main`. `git log`
reads as a sequence of complete changes rather than a braid, which matters more than usual here:
with no reviewer, the log *is* the review record.

**A PR per milestone, opened early and used as a record.** Solo work still gets a pull request
against `main`, with the template filled in properly — summary, test plan with each box actually
justified, and a real risks-and-rollback section. It is not an approval gate; nobody else is going
to click merge. It is the place where the reasoning, the manual verification, and the blast radius
get written down while they are still fresh, and it stays open while the branch accumulates
commits. Reviewing your own PR before merging it catches a surprising amount.

**Commits carry the reasoning.** Subjects are `type(scope): imperative summary`; bodies explain
why the change is needed and what it means for a user, wrapped at 72 characters. Recent history is
the reference — `git log` shows the shape. Opt into the template once per clone:

```sh
git config commit.template .gitmessage
```

Before pushing, run what CI runs:

```sh
gofmt -l .          # must print nothing
go vet ./...
go test -race ./...
```

CI also cross-compiles for linux and darwin on amd64 and arm64. The binary is cgo-free and must
stay that way — a dependency needing cgo breaks static builds and cross-compilation, which is why
the SQLite driver is `modernc.org/sqlite` and the PDF converter is a pure-Go library.

## If you are contributing from outside

The process is the same one described above — branch, commits that explain themselves, a PR with
the template filled in. The only difference is that someone else reads it. Open an issue first if
the change is substantial, so the design discussion happens before the work.

The bar below is the same bar the maintainer's own commits are held to.

## What gets a change rejected or reverted

Most of these come from bugs this repo has already shipped. They apply equally to a PR and to a
commit I am about to push myself.

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
point of the commit, argued in the message, not a quiet edit buried in an unrelated change.

**Blocking I/O in a Bubble Tea `Update`, or writing to stdout from the TUI.** The first stalls the
display behind a slow Wi-Fi link; the second corrupts the frame. Return a `tea.Cmd`, and log to
`$XDG_STATE_HOME/shelf/shelf.log`.

**Hand-editing generated or captured files.** Golden frames come from
`go test ./internal/tui/ -update-golden`. Device fixtures in `internal/device/testdata/` are real
hardware captures — re-capture rather than edit, and anonymize the `ip` field and any filenames,
which otherwise publish a home network address and somebody's reading list.

**A new dependency that is not worth its weight.** Each one is a future packaging problem, and
anything requiring cgo is disqualified outright. The list is deliberately short.

**Assuming API behavior the firmware does not have.** No mtime, no checksum, no upload resume, and
a missing directory returns `200 []` rather than `404`. Tests assert these gaps stay gaps. If a
future firmware fills one in, that is its own change with its own reasoning, not an incidental edit.

**Unrelated formatting churn.** It hides the real change in the log, which is the only review this
code gets.

## Testing expectations

New behavior needs a test that would fail without it. With no reviewer, the test suite is the
safety net, so this is not negotiable for anything touching sync, device I/O, or EPUB writing.

- Logic that can be pure should be pure and table-driven. `internal/sync/plan_test.go` is the model.
- Device behavior is tested against the fakes in `internal/device`: an `httptest` server and a
  WebSocket peer replaying every documented `ERROR:` string. Add new firmware error strings there
  and in `device/errors.go`, rather than matching text at call sites.
- Hardware tests live behind the `hardware` build tag and must skip cleanly with no device present.
- Anything touching filenames needs a non-ASCII case. NFC normalization and display-width
  alignment are both real bugs that only surface with accented or CJK text.

## Style

- Format with `gofmt`; CI fails on any output from `gofmt -l .`.
- Don't reformat unrelated lines; keep diffs minimal.
- Comments explain WHY, not WHAT — especially where code looks strange because the firmware is
  strange. One comment naming the device behavior that forced an odd branch is worth more than
  three restating the syntax.

## Reporting device bugs

Firmware misbehavior, as opposed to a shelf bug, belongs upstream at
[crosspoint-reader/crosspoint-reader](https://github.com/crosspoint-reader/crosspoint-reader).
Search first — several web-endpoint stability issues are already open. Include the firmware version
from `GET /api/status`, and scrub your LAN address and book filenames before posting.
