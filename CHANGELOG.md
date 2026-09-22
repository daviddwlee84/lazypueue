# Changelog

## Unreleased

## [0.1.2] - 2026-09-23

- CLI and TUI upgrades can delegate verified Homebrew installations to their owning formula, retaining confirmation and check-only behavior. Results verify the stable `opt` target and report the actual version, including manager no-ops.

## [0.1.0] - 2026-09-21

- First public release of the Pueue terminal dashboard and CLI.
- Checksummed macOS/Linux amd64/arm64 archives with shell completions.
- Standalone archive upgrades preserve the existing executable on verification failures; source and package-manager paths remain separate.
