# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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
