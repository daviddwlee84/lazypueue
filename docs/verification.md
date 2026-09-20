# Verification

Recorded on 2026-09-20 on macOS arm64, using Go 1.27.0 and Pueue 4.0.2. The module declares Go 1.25. These results describe local verification, not a completed CI run.

## Automated checks

| Command | Result |
| --- | --- |
| `go test -race ./...` | Passed for CLI, config, core, forms, Pueue adapter, and dashboard packages. |
| `go vet ./...` | Passed. |
| `GOOS=linux GOARCH=amd64 go build -o /tmp/lazypueue-linux .` | Passed cross-build; Linux runtime behavior remains unverified locally. |
| `go build -o /tmp/lazypueue-verification .` | Passed; help and JSON dry-run/error output exercised with the built application. |
| `LAZYPUEUE_INTEGRATION=1 go test ./internal/pueue -run TestIsolatedDaemonIntegration -v -count=1` | Passed against separate disposable Unix-socket and TLS daemons. |

The normal Go test run skips real-daemon integration unless `LAZYPUEUE_INTEGRATION=1` is set. Integration tests require both `pueue` and `pueued` on PATH. Each daemon uses a fresh private directory, explicit configuration, independent credentials/socket, and isolated task history; the tests do not address the user's default queue.

Coverage includes:

- CLI bare-command/wizard decisions, invalid flags before prompting, explicit config paths, confirmation, exact reviewed IDs and creation-time guards, successful-only cleanup, pure dry runs, structured stdout/stderr, and partial failures across connections.
- Config defaults and XDG paths, validation, comment/unknown-field preservation, concurrent-write rejection, private file modes, and state persistence.
- Forms owning printable input and pasted multiline commands, explicit review/submit, Back/cancel, async draft connection tests, stale read results, retained drafts after failure, and dependency identity guards.
- Task parsing, terminal failure variants, omission of captured environments, safe argv/SSH quoting, native config generation, stale task protection, and uncertain write outcomes.
- Dashboard selection by connection and task identity, equal IDs on separate hosts, stale response rejection, filtering, multiselection boundaries, captured action targets, bounded logs, Unicode/resize behavior, and input ownership.

The isolated daemon workflow exercised task submission and dependencies; group pause/start and parallelism; stash/enqueue; pause/resume/kill; restart as new, in place, and failed batches; cleanup of captured IDs; refusal to remove nonempty groups; empty-group removal; and native log/follow. It also checked bad shared-secret and invalid TLS-certificate failures, negative priority, and dash-leading labels/group names. Both transport cases passed with Pueue 4.0.2.

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

Both checks passed locally. They exercised task and connection wizards, multiline paste containing shortcut characters, Enter without submission, review/Back/submit, cancellation, and connection testing without saving. The dashboard run also covered multi-host identical IDs plus an offline host, navigation and search, the action menu, dependent/new-task cancellation, log/follow and paused autoscroll, and sizes 120×36, 80×24, 38×12, and 12×4. Assertions checked that browsing/cancellation caused no mutation and that terminal modes were restored on exit.

With the default output directory, dashboard screen text is exported as `/tmp/lazypueue-pty-120.txt`, `-80.txt`, `-38.txt`, and `-12.txt`. These are inspection aids, not proof of every terminal's glyph rendering; the model tests separately check Unicode content and cell widths.

## Remaining verification limits

- No real remote SSH server was changed or used for an end-to-end remote mutation test. SSH argv/routing/quoting and failure behavior are covered by fixtures; authentication, jump hosts, and user-specific remote environments still need a real-host check.
- Native Unix/TLS tests ran locally against disposable daemons, not across an actual network.
- A Linux PTY session has not been run locally. The configured GitHub Actions matrix covers Linux and macOS, including the terminal scripts, but its first run has not been observed.
- Testing with the declared minimum Go 1.25 is delegated to that CI configuration; the local toolchain was Go 1.27.0.
