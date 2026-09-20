# lazypueue

A Go terminal dashboard for [Pueue](https://github.com/Nukesor/pueue): see queues across machines, inspect task output, restart jobs, and compose new jobs without remembering every `pueue add` flag.

The interaction follows [Pueue Raycast Extension](https://github.com/daviddwlee84/Pueue-Raycast-Extension), with a compact overview inspired by btop and keyboard navigation like the other lazy tools. It uses the installed Pueue CLI and your existing daemons.

## Run

Requires Go 1.25 or newer to build and Pueue 4.x on each machine that owns a queue. SSH connections use your system `ssh` and SSH configuration.

```sh
go build -o lazypueue .
./lazypueue

# Or install this checkout into GOBIN / GOPATH/bin:
go install .
```

No lazypueue configuration file is required for the local queue. A stopped daemon appears as an offline connection; the app does not automatically start or reconfigure daemons.

```sh
lazypueue connections add                   # connection wizard
lazypueue connections add lab --kind ssh --ssh-host lab-server
lazypueue connections test lab
lazypueue --connection lab
```

## Dashboard

The overview keeps per-connection health, task counts, group capacity, tasks, and the selected task's detail/log preview visible. Tasks and Groups each retain their selection and filters across refreshes. A failed refresh retains the previous snapshot with its age and error.

| Key | Action |
| --- | --- |
| `↑` / `↓`, `j` / `k` | Move within the focused pane |
| `Tab` / `Shift+Tab`, `h` / `l` | Switch panes |
| `gg` / `G` | First / last row |
| `0` / `1` / `2` / `3` / `4` | Scope / Tasks / Groups / Detail-log / Monitor |
| `/` | Search tasks/groups |
| `s` | Filter by status |
| `Enter` | Open task log; drill into a group |
| `Space` | Select tasks for a batch action within one connection |
| `W` | Add selected tasks to Monitor; repeat on another connection for cross-host monitoring |
| `n` | Add-task wizard |
| `a` | Add a job depending on the selected task |
| `p` | Pause a running task or resume a paused task |
| `R` / `I` | Review restart as a new task / in place |
| `e` | Edit a task; completed/running tasks use a reviewed retry workflow |
| `Y` | Copy a failure report with task metadata and recent output |
| `F` | Follow task output |
| `t` | Manage connections and choose a connection or All |
| `:` | Search available actions, including copy, duplicate, cleanup, and group controls |
| `r` | Refresh |
| `?` | Contextual help |
| `m` | Toggle mouse capture (enabled by default) |
| `Esc` | Back / close the current surface |
| `q` | Quit from the dashboard |

Task actions are enabled according to current task state. The action menu includes restart in place, force-start, stash/enqueue, kill/remove, copying the command or a reproducible `pueue add`, duplicate-and-edit, and adding dependent work. Group actions include creation, pause/resume, concurrency, restart failed jobs, cleanup, and clearing the reviewed tasks while retaining the group. Destructive actions and every restart require a default-No review: `y` applies; `Enter`, `n`, or `Esc` cancels. Mouse clicks focus panes, select rows and operate visible buttons; the wheel scrolls the pane beneath it.

The inline detail and expanded log share one collector. `L` selects Live, Polling, or Manual and a refresh interval; `Space` pauses collection, `r` reads once, and `G` returns to the tail. Local/native single-task logs default to live; SSH and multi-task Monitor default to polling every 10 seconds. `PgUp`/`PgDn` and `Ctrl+U`/`Ctrl+D` scroll; `/` searches and `n`/`N` change matches. `y` copies loaded plain text, `Y` copies a failure report, `i` shows metadata, and `o` opens the full log in a pager. Safe original colors and severity coloring improve scanability. See [log monitoring and configuration](docs/logs.md).

Queue/group progress means **finished tasks / retained tasks**, including failures. Running-task progress is separate: it appears only when a recognizable tqdm bar or explicit progress record exists in the output; `i` shows the matched source and observation time. Estimated group time uses observed task durations and is unavailable when there is not enough evidence or concurrency is unlimited.

## Task wizard

Use `n` in the dashboard or bare `lazypueue add` in a terminal. Both use the same form:

1. Choose connection, complete shell command, daemon-side working directory, group, label, and start mode.
2. `Ctrl+O` reveals dependencies, delay, priority, and optional group creation/concurrency.
3. `Ctrl+S` opens the review; a second `Ctrl+S` submits. `Esc` returns to the draft; `Ctrl+C` cancels.

`Enter` inserts a newline in the command field. `Tab` / `Shift+Tab` change fields. `Ctrl+L`, `Ctrl+G`, and `Ctrl+D` open connection, group, and dependency pickers. Successfully used directories and groups are remembered per connection; commands and logs are not written to UI state.

Connection setup uses the same review/submit pattern. `Ctrl+T` tests a connection draft without saving it. Editing a connection preserves unrelated configuration fields and comments.

Task editing (`e` or `lazypueue edit ID`) starts from the original command and edits command, directory, label and priority. Queued/stashed tasks keep their IDs. Completed tasks default to a new ID, preserving the original environment and logs; advanced options allow in-place retry or keeping the result stashed. Running/paused tasks are stopped only after the draft's final `y` review. Interrupted native edits can be recovered with “Recover locked task to stash” in the action menu.

## Scriptable commands

```sh
lazypueue status --connection all --json
lazypueue status --connection lab --group ml --state failed
lazypueue group list --connection all --json
lazypueue log 42 --connection lab --lines 500
lazypueue log 42 43 --connection lab --lines 200 --json
lazypueue log 42 --connection lab --full
lazypueue follow 42 --connection lab

# Pass the complete shell command as ONE argument.
lazypueue add --label train-seed1 --group ml --create-group --group-parallel 4 \
  -- 'python train.py --seed 1'
lazypueue add --connection lab --directory /srv/project --group ml \
  --after 40,41 --delay 30m -- 'python evaluate.py'
lazypueue add --interactive --group ml     # prefilled wizard
lazypueue add --dry-run --json -- 'echo hello'

lazypueue pause 42
lazypueue start 42 --yes                   # force-start may skip dependencies/slots
lazypueue restart 42                      # new ID; old logs remain
lazypueue restart 42 -i --yes              # same ID; overwrites logs
lazypueue restart 42 -e                    # edit and review before retrying
lazypueue edit 43 --command 'python corrected.py' --yes
lazypueue restart-failed --group ml --yes
lazypueue stash 43
lazypueue enqueue 43
lazypueue kill 42 --yes
lazypueue remove 42 --yes
lazypueue clean --group ml --yes           # successful tasks only
lazypueue clean --successful-only=false --yes
lazypueue group add ml
lazypueue parallel 4 --group ml --yes       # 0 means unlimited
lazypueue group pause ml --yes
lazypueue group start ml --yes
lazypueue group remove ml --yes            # only empty groups can be removed
lazypueue group clear ml --yes             # stop/remove reviewed tasks; keep group
```

Add `--connection ID` to target a specific queue. With no connection flag, single-target commands use the configured default, or the first connection when the default is `all`. Explicit `--connection all` is supported by `status` and `group list`; it cannot broadcast mutations.

Global flags are `--config`, `--connection`, `--json`, `--interactive`, `--yes`, and `--dry-run`. `--dry-run` never applies changes. Ordinary task/group previews perform no daemon probes; edit previews read the existing task to retain unspecified fields, and upgrade previews inspect versions and installation state. Confirmed operations use `--yes` for scripts. `follow` streams plain text and does not support JSON.

Bare `add`, `connections add`, and `edit ID` start their form in a terminal; `connections edit ID` with no field flags opens its edit form. Partial business flags without `--interactive` produce an error when required data is missing. Pipes and `--json` never prompt. Invalid flags are rejected before starting a form. Use the long `--interactive` flag; `restart -i` means in-place, matching Pueue.

JSON data goes to stdout; errors go to stderr as `{"error":"...","exit_code":2}` when `--json` is parsed. `status --connection ID --json` emits one sanitized snapshot; `status --connection all --json` emits a `connections` array containing each snapshot or error. A partial connection failure preserves successful results and returns exit 1. Task environments are omitted from snapshots and log JSON.

Exit codes: `0` success, `1` operation failure, `2` usage/configuration error, `130` cancellation. If a connection drops during a write, its outcome can be unknown; refresh the queue before retrying. Cancellation cannot undo a task already accepted by Pueue.

## Connections and configuration

Preferences use `$XDG_CONFIG_HOME/lazypueue/config.toml`, falling back to `~/.config/lazypueue/config.toml` on macOS and Linux. Override with `--config PATH` or `LAZYPUEUE_CONFIG`. Explicit missing files are errors; for a new custom path, run `lazypueue --config PATH config edit` first. UI state uses `$XDG_STATE_HOME/lazypueue/state.json`, falling back to `~/.local/state/lazypueue/state.json`.

```sh
lazypueue config show
lazypueue config show --json
lazypueue config edit                     # VISUAL, then EDITOR, then vi
lazypueue connections list --json
lazypueue connections edit lab --ssh-host another-alias
lazypueue connections remove lab --yes
```

See [examples/config.toml](examples/config.toml) for local, SSH, and native connection definitions.

**SSH** runs Pueue on the remote machine, preserving remote directory and environment semantics. Normal SSH host-key checks, aliases, proxy jumps, and agents remain in effect. Background operations do not prompt for passwords. Authenticate with your usual SSH workflow, or use the dashboard's “Authenticate SSH and reconnect” action. Pueue can be configured with an explicit executable path when remote PATH discovery does not find it.

**Native** connections reference an existing Pueue config/profile, an explicit TLS endpoint (host, port, certificate path, and shared-secret path), or a forwarded Unix socket with its shared-secret path. Lazypueue creates a private temporary client configuration with remote log reads enabled; it does not modify your Pueue configuration or store credential contents in its preferences. Unix-socket forwarding itself is managed by your existing SSH workflow.

Native task submission requires an SSH companion (`ssh_host`, with optional `ssh_config` / `ssh_profile`). Native Pueue submission captures the local client's environment; Pueue 4.0.3 and 4.0.4 also resolve the working directory locally. Lazypueue consistently uses the SSH companion for submission so the directory and environment come from the remote host. Reads and controls continue using the native connection. A connection without a companion can still inspect logs and control existing tasks.

Pueue dependencies wait for **all parents to succeed**. Failed parents result in `DependencyFailed`; the dashboard displays spawn failures and other non-success terminal results as failures. This app manages existing queues and does not distribute jobs between hosts or create a new scheduler. Daemon services change only through explicitly reviewed backend maintenance.

## Upgrades

```sh
lazypueue upgrade --check --json
lazypueue upgrade --yes
lazypueue backend status --connection all --json
lazypueue backend upgrade --connection lab --check
lazypueue backend upgrade --connection lab --yes
```

Self-upgrade builds a captured stable source release and atomically replaces the same resolved executable. Development copies need `--force`; package-owned or unknown copies are preserved. With no published stable release, check reports `source-unavailable`.

Backend upgrades support verified Homebrew/Cargo ownership and a matching Homebrew or systemd user service. They require an idle queue, preserve paused groups, record every maintenance phase, and refuse active work even with `--yes`. Native connections require an SSH companion for administration. Read [upgrade behavior and recovery](docs/upgrades.md) before maintaining a daemon.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

CLI, dashboard, and forms use the same backend validation and operations. Tests use isolated configuration/state and fake transports; the application's normal startup does not submit test tasks. Generate completions with `lazypueue completion bash`, `zsh`, `fish`, or `powershell`.

See [verification](docs/verification.md) for observed results, disposable-daemon integration tests, and real PTY scripts. The shared-form PTY test uses only Python's standard library; the dashboard screen checks require `pyte==0.8.2`.

Other references: [PueueWrapper](https://github.com/daviddwlee84/PueueWrapper), [lazymlflow](https://github.com/daviddwlee84/lazymlflow), and [lazyclash](https://github.com/daviddwlee84/lazyclash).
