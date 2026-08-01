## Summary

<!-- One paragraph: what changed and why. -->

## Test plan

- [ ] A test covers the change and would fail without it
- [ ] `gofmt -l .` prints nothing, `go vet ./...` and `go test -race ./...` pass
- [ ] Manual smoke check (if UI or device behavior changed)
- [ ] Docs updated (if a public surface changed)
- [ ] No unrelated formatting churn in the diff

<!-- Delete whichever does not apply: -->
- [ ] This change cannot move an already-pinned book to a new device path
- [ ] N/A — does not touch sync planning

## Risks / rollback

<!-- What could go wrong; how to revert if it does. -->
