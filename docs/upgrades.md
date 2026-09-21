# Upgrading lazypueue and Pueue

These are separate operations. Updating lazypueue replaces the local dashboard executable. Updating a Pueue backend changes an installation on its host and restarts one verified daemon service.

## The dashboard executable

```sh
lazypueue upgrade --check --json
lazypueue upgrade                     # review in a terminal
lazypueue upgrade --yes               # explicit noninteractive approval
lazypueue upgrade --force --yes       # replace a recognized development build
```

The self-updater requires a formal stable GitHub release. Standalone release archives download the exact platform archive and verify checksums before candidate inspection and replacement. Source installations build its exact tag with the installed Go command. Until a release exists, check reports `source-unavailable`; it does not substitute the main branch or change the checkout. Version output uses an injected build version, then Go's embedded module version, then `dev`.

The source package is `github.com/daviddwlee84/lazypueue` at the repository root. Release archives cover macOS/Linux amd64/arm64. Archive updates require no Go installation; an absent archive, failed download, or checksum mismatch never falls back to source. Go keeps the user's `GOTOOLCHAIN` policy, which may download a compatible toolchain. A missing Go command is reported rather than installed automatically.

Inspection separates build provenance from package ownership. Homebrew, Nix, and mise installations receive manager guidance; unknown binaries are preserved. Recognized local/development builds need `--force`. That flag never bypasses ownership, executable identity, or candidate verification.

Apply builds in private staging next to the resolved current executable, validates package/module/platform/version, rechecks the original file and launch symlinks, and publishes with an atomic rename. A different copy earlier on PATH or a changed GOBIN cannot redirect the update. Failed builds, cancellation, concurrent updates, and changed destinations retain the original. Start a new invocation to use the updated binary.

`--check` and `--dry-run` do not create staging, replace files, access a queue, load preferences, or refresh skills. Normal startup, help, and version do not check the network. JSON results remain on stdout; errors use the structured stderr envelope.

## Pueue installations and daemon services

```sh
lazypueue backend status --connection all --json
lazypueue backend upgrade --connection local --check
lazypueue backend upgrade --connection lab --dry-run --json
lazypueue backend upgrade --connection lab --yes
```

The check shows the resolved binaries, installed and candidate versions, package ownership, matching daemon service/PID, queue activity, and the exact maintenance commands. `all` is check-only. Apply always targets one connection and needs one final review or `--yes`.

Supported automatic paths are:

| Installation | Ownership evidence | Upgrade command |
| --- | --- | --- |
| Homebrew | Resolved Cellar keg, installation receipt, matching formula metadata | The owning `brew upgrade <formula>` |
| Cargo/crates.io | `.crates.toml` receipt matching both binaries and installed version | `cargo install --root <actual-root> --locked --version <reviewed-version> pueue` |

The service must be a matching registered Homebrew service or `pueued.service` in the user's systemd manager. Its executable, config/profile, and PID must match the selected Pueue configuration's daemon PID file. A custom configuration needs an absolute path. Unknown ownership, unmatched supervisors, missing PID evidence, other package managers, and unsupported Pueue majors produce instructions instead of guessed writes. The current compatibility boundary is Pueue 4.x.

Local and SSH connections are supported. A native connection needs an explicit SSH companion for host administration; maintenance checks the companion's queue and service. A native-only protocol connection cannot install host packages. SSH checks use normal host-key policy and noninteractive authentication; they do not prompt for passwords or call sudo.

### Idle-only maintenance

Pueue daemon shutdown kills child tasks; “graceful” shutdown is not a queue drain. Consequently maintenance refuses any running, paused, locked, or unknown task before changing scheduling. `--yes` cannot override this condition. It does not wait for heavy jobs, force-kill them, or secretly drain a queue.

After approval, apply:

1. Rechecks the captured installation, exact candidate, service identity, group state, and queue activity.
2. Acquires a host/package lock and starts a private durable receipt.
3. Uses `pueue pause --all --wait` to stop new scheduling without suspending processes, then checks activity and groups again.
4. Stops the verified service, upgrades through its owner, verifies both installed binaries, and starts that same service.
5. Verifies a new daemon PID and a healthy matching queue, then resumes only groups that were running before maintenance. Previously paused groups remain paused.

This protects ordinary queue scheduling and cooperating lazypueue updaters. Pueue has no exclusive maintenance RPC; external clients must not force-start work or reconfigure the daemon during the reviewed operation. The package installation may be shared by several daemon profiles; only the matched service is restarted.

During backend maintenance, this dashboard blocks task and connection mutations across all configured connections, since different aliases can address the same daemon. Returning to the dashboard does not release that guard; the actual maintenance result does.

### Partial failures and receipts

Receipts live in `$XDG_STATE_HOME/lazypueue/maintenance/` (default `~/.local/state/lazypueue/maintenance/`) with mode 0600. They record the reviewed target and commands, started/completed/failed steps, and whether package update or daemon startup completed. Credential contents and raw manager output are not recorded.

If maintenance fails after pausing queues or stopping the service, its result explicitly reports the partial state. It does not claim to roll back a package manager. Inspect the receipt and the target host before retrying; scheduling can remain paused or the service can remain stopped. Interrupted SSH writes have an unknown outcome and retain the remote `/tmp/lazypueue-maintenance-…` lock. Verify no maintenance process remains and inspect the receipt before manually removing that named lock directory.

No real package upgrade or daily-host service restart is performed by the test suite. See [verification](verification.md) for disposable transport and transaction tests.
