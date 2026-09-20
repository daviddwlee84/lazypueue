# Verification

Recorded on 2026-09-20 on macOS arm64, using Go 1.27.0 and Pueue 4.0.2. The module declares Go 1.25. These results describe local verification, not a completed CI run.

## Automated checks

| Command | Result |
| --- | --- |
| `go test -race ./...` | Passed for CLI, config, core, forms, log ingestion, Pueue workflows, dashboard/Monitor, self-update, and maintenance. |
| `go vet ./...` | Passed. |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/lazypueue-linux .` | Passed cross-build; Linux runtime behavior remains unverified locally. |
| `go build -o /tmp/lazypueue-v2 .` | Passed; help, example configuration and JSON output exercised with the built application. |
| `LAZYPUEUE_INTEGRATION=1 go test -race ./internal/pueue -run TestIsolated -v -count=1` | Passed against separate disposable Unix-socket/TLS daemons and a controlled POSIX SSH editor fixture. |
| `LAZYPUEUE_SSH_INTEGRATION=1 go test ./internal/maintenance -run TestDisposableSSHTransport -v -count=1` | Passed against a disposable loopback sshd with isolated keys/configuration. |

The normal Go test run skips real-daemon integration unless `LAZYPUEUE_INTEGRATION=1` is set. Integration tests require both `pueue` and `pueued` on PATH. Each daemon uses a fresh private directory, explicit configuration, independent credentials/socket, and isolated task history; the tests do not address the user's default queue.

Coverage includes:

- CLI bare-command/wizard decisions, invalid flags before prompting, explicit config paths, confirmation, exact reviewed IDs and creation-time guards, successful-only cleanup, previews without writes, structured stdout/stderr, and partial failures across connections. Ordinary task/group dry runs do not probe; edit/upgrade previews use read-only checks.
- Config defaults and XDG paths, validation, comment/unknown-field preservation, concurrent-write rejection, private file modes, and state persistence.
- Forms owning printable input and pasted multiline commands, explicit review/submit, Back/cancel, async draft connection tests, stale read results, retained drafts after failure, and dependency identity guards.
- Task parsing, terminal failure variants, omission of captured environments, safe argv/SSH quoting, native config generation, stale task protection, and uncertain write outcomes.
- Dashboard selection by connection and task identity, equal IDs on separate hosts, stale response rejection, filtering, multiselection boundaries, captured action targets, bounded logs, Unicode/resize behavior, and input ownership.
- Shared inline/fullscreen log sessions, same-host batching, cadence, hidden-page cancellation, snapshot replacement, reading/search freezes, explicit refresh while paused, superseding older-tail requests, attempt changes, eviction/recreation, endpoint replacement, and retained watch preferences.
- ANSI/UTF-8 chunks split across reads, carriage-return progress, bounded source-style storage, safe SGR retention, OSC/control stripping, semantic coloring, strict progress detection, plain bounded failure reports, external pager failures and stale handoff cancellation.
- Default-No keyboard/mouse reviews, mouse press/release/resize ownership, repeated/pasted input protection, backend-maintenance serialization across connection aliases, and stale/duplicate upgrade results.
- Self-update source-unavailable behavior, version metadata, pinned release plans, moved executables/symlinks, manager/development protection, candidate verification, cancellation, destination changes, concurrent updates, and failed publication.
- Backend ownership receipts, service/PID matching, idle refusal before scheduling changes, activity appearing after pause, candidate/group changes after review, original paused-group preservation, correct Cargo install root, partial-failure receipts, and retained locks for uncertain SSH writes. Manager/service mutations use injected fixtures, never the user's installations.

The isolated daemon workflows exercised task submission and dependencies; group pause/start and parallelism; stash/enqueue; pause/resume/kill; restart as new, in place, and failed batches; native four-field editing, environment preservation, stop/edit/retry and Locked recovery; cleanup of captured IDs; group clear with dependency protection and newcomer preservation; refusal to remove nonempty groups; empty-group removal; and batch log/native follow. They also checked bad shared-secret and invalid TLS-certificate failures, negative priority, and dash-leading labels/group names. Both Unix and TLS cases passed with Pueue 4.0.2. The POSIX SSH editor fixture is separate from the real loopback SSH transport test.

Deterministic resource tests use eight connections with two tasks each: the blocking batch adapter observes at most four concurrent reads and one per connection, with no queued catch-up reads. In an unchanged-tail fixture, two polling snapshots return 17,600 output bytes versus 8,800 initial bytes from two quiet live streams. These counters exclude JSON/SSH/TLS framing and are not network benchmarks; they demonstrate why a longer polling interval controls cadence without guaranteeing fewer bytes than Live.

## Real terminal checks

Build once, then run the shared-form check with the Python standard library:

```sh
go build -o bin/lazypueue .
python3 scripts/form_pty_smoke.py --binary bin/lazypueue
```

The dashboard check requires `pyte==0.8.2` to inspect the current rendered screen. Raw stdout alone cannot reconstruct Bubble Tea's incremental updates:

```sh
uv run --no-project --with pyte==0.8.2 python scripts/pty_smoke.py \
  --binary bin/lazypueue
```

Alternatively, install `pyte==0.8.2` into a Python environment and run the script with that environment's interpreter. These scripts use disposable fake Pueue executables and isolated child-process HOME/XDG directories.

Both checks passed locally. They exercised task and connection wizards, multiline paste containing shortcut characters, Enter without submission, review/Back/submit, cancellation, and connection testing without saving. The dashboard run also covered multi-host identical IDs plus an offline host; shared inline/fullscreen live output; progress and metadata; source ANSI cell colors and resets; Monitor batching, modes, manual reads, pause and persistence; direct panes and paging; mouse forms and disabled capture; plain clipboard failure reports; edit cancellation; and default-No/in-place restart with exactly one reviewed mutation. Sizes 120×36, 80×24, 38×12 and 12×4 were inspected. Terminal modes were restored on exit.

With the default output directory, dashboard screen text is exported as `/tmp/lazypueue-pty-120.txt`, `-80.txt`, `-38.txt`, and `-12.txt`. These are inspection aids, not proof of every terminal's glyph rendering; the model tests separately check Unicode content and cell widths.

## Remaining verification limits

- `LAZYPUEUE_SSH_INTEGRATION=1 go test ./internal/maintenance -run TestDisposableSSHTransport -v -count=1` passed against a temporary loopback sshd with generated host/user keys, isolated configuration and pinned known_hosts. It verifies real authentication, literal argv/Unicode/newline transport, and remote error propagation. No daily remote host or package manager was changed; jump hosts and real remote service maintenance remain untested.
- The self-updater reports `source-unavailable` against the current public release endpoint; transaction tests use pinned fake release/build fixtures until a formal source release is published. No real lazypueue executable was replaced.
- Native Unix/TLS tests ran locally against disposable daemons, not across an actual network.
- A Linux PTY session has not been run locally. The configured GitHub Actions matrix covers Linux and macOS, including the terminal scripts, but its first run has not been observed.
- Testing with the declared minimum Go 1.25 is delegated to that CI configuration; the local toolchain was Go 1.27.0.
