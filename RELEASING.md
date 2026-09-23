# Releasing lazypueue

Stable tags use `vMAJOR.MINOR.PATCH` and are immutable. Push the release commit to
`main`, wait for CI, then tag that exact commit. Do not move an existing tag.

The release workflow reruns this repository's CI at the selected tag, verifies
that the tag is an ancestor of `origin/main`, and builds with GoReleaser 2.18.2.
It publishes only after the version-specific archive inventory and SHA-256
manifest have been uploaded to a draft and downloaded again for verification.

## Artifact contract

- Starting at v0.2.0: Darwin/Linux/Windows, amd64/arm64, with `CGO_ENABLED=0`.
- Darwin/Linux archives: `lazypueue_<version-without-v>_<os>_<arch>.tar.gz`.
- Windows archives: `lazypueue_<version-without-v>_windows_<arch>.zip`.
- Each archive contains the flat `lazypueue` executable (`lazypueue.exe` on Windows),
  `LICENSE`, and Bash, Zsh and PowerShell files under `completions/`.
- `lazypueue_<version-without-v>_source.tar.gz` is filtered by `.gitattributes`.
- New releases have six binary archives, one source archive and `checksums.txt`:
  eight assets; the checksum manifest names all seven archives.
- Historical tags keep the version-bound inventory in `scripts/release.py`.
  Do not add Windows files to an existing release or replace immutable assets.
- Linker metadata carries the exact stable tag; verification checks product,
  version, architecture, PE/ELF/Mach-O headers and safe archive membership.
- Homebrew and Scoop publication is owned by their central repositories. The
  product release workflow does not write package manifests directly.
- Required Windows CI runs native Go tests and real isolated Scoop check,
  process-exit handoff, update, no-op, running-instance and checksum-failure cases.

## Verify without publishing

```sh
python3 -m unittest discover -s scripts -p "test_*release*.py"
goreleaser check
goreleaser release --snapshot --clean
python3 scripts/release.py check --project lazypueue --binary lazypueue --smoke
```

Snapshots are local build artifacts, not stable releases. Native smoke checks
verify startup on the build host; header checks verify all target architectures.

## Retry

Rerun the failed GitHub Actions run, or dispatch `Release` with the existing tag.
Matching assets are retained; missing assets are uploaded only while the release
is still a draft. A differing existing asset or an incomplete already-public
release stops the job without replacement. Never delete or overwrite assets as
an automatic recovery step. Resolve the discrepancy explicitly or publish a new
version.

Builds use a fixed GoReleaser version, retained linker/VCS metadata, and commit timestamps. Official archive builds do not use `-trimpath`, because Go omits the `-ldflags` build setting required for offline release-stamp inspection when that flag is set.
Changing the Go toolchain or release inputs may produce different bytes; the
retry guard intentionally refuses to overwrite them.

The initial binary tag predates this replay-safe publisher. Its historical workflow is immutable; do not move that tag or overwrite its assets. This workflow governs subsequent tags. The initial published assets were downloaded and verified separately.
