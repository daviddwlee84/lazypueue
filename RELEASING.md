# Releasing lazypueue

Stable tags use `vMAJOR.MINOR.PATCH` and are immutable. Push the release commit to
`main`, wait for CI, then tag that exact commit. Do not move an existing tag.

The release workflow reruns this repository's CI at the selected tag, verifies
that the tag is an ancestor of `origin/main`, and builds with GoReleaser 2.18.2.
It publishes only after all four binary archives, the filtered source archive,
and their SHA-256 manifest have been
uploaded to a draft and downloaded again for verification.

## Artifact contract

- Targets: Darwin/Linux, amd64/arm64, with `CGO_ENABLED=0`.
- Archives: `lazypueue_<version-without-v>_<os>_<arch>.tar.gz`.
- Each archive contains the flat `lazypueue` executable, `LICENSE`, and
  `completions/lazypueue.bash` / `completions/lazypueue.zsh`.
- New tags include `lazypueue_<version-without-v>_source.tar.gz`, a rootless source archive filtered by `.gitattributes`.
- `checksums.txt` names exactly those five archives; tags before v0.1.1 retain their four-archive contract.
- The linker injects the Git tag into `main.version`.
- Homebrew publication is managed centrally; this workflow does not write to a tap.

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
