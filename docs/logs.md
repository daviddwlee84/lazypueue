# Logs and multi-task monitoring

Select task rows with `Space`, then press `W` to add them to Monitor. Repeat from another connection to monitor multiple hosts. The watchlist is separate from the connection-scoped selection used for batch mutations. `4` opens Monitor, `Tab` changes tile, `[`/`]` changes page, `Enter` expands the focused tile, and `x` removes a watch. Wide terminals show four tiles, 80-column terminals show two stacked tiles, and narrower terminals show one.

The inline detail, expanded log and Monitor tile reuse the same task session. Opening an expanded log does not create a duplicate live stream. Watch identity includes connection, task ID and creation time; an ID reused after removal cannot silently substitute another task. A restarted attempt clears the previous attempt's output and progress.

## Collection and reading

`L` opens collection settings for the focused task:

| Mode | Behavior |
| --- | --- |
| Live | Stream running output with Pueue follow; retain a bounded history. |
| Polling | Replace the visible tail with a fresh snapshot at the configured interval. |
| Manual | Read only when explicitly refreshed with `r`. |

Local/native single-task sessions default to Live. SSH single-task sessions and all multi-task sessions default to Polling every 10 seconds. Presets are 2, 5, 10, 30 and 60 seconds; custom durations such as `2m` are accepted (minimum 1 second).

`Space` pauses/resumes collection. `r` explicitly refreshes once, including while paused. `f` enters Live, or toggles autoscroll if already Live. Scrolling or searching in Polling freezes the loaded snapshot so a subsequent poll cannot replace what you are reading. `G` returns to the tail and resumes polling unless collection is paused. Live scrolling can leave autoscroll off while collection continues.

Use `PgUp`/`PgDn` for pages, `Ctrl+U`/`Ctrl+D` for half pages, and `/` followed by `n`/`N` for search. `PgUp` again at the top loads a larger tail and freezes it for reading. `o` streams the complete output to `less -R`; exiting the pager returns to the dashboard. The header reports mode, pause/reading state, observation age and errors.

`i` opens task metadata, including the exact log record behind detected task progress. Queue/group bars count retained terminal tasks, including failures. A running-task bar is shown only for recognized tqdm or explicit progress output; generic percentages such as accuracy or learning rate are not treated as task completion.

## Copying and colors

- `y` copies the loaded plain log, which may be only a tail.
- `Y` fetches a failure report containing connection, task metadata, result/exit code and up to the last 500 lines, bounded to 256 KiB.
- Copy uses the terminal's OSC 52 clipboard support. Terminal policy can disable clipboard writes.
- The reader preserves allowlisted SGR colors/basic styles, strips other terminal controls, and recognizes error/warning/info severity. Carriage-return progress updates replace the current line. Copying and reports remain plain text. `NO_COLOR` disables display coloring.

## Configuration and resource use

```toml
[logs]
single_mode = "auto"
multi_mode = "poll"
poll_interval = "10s"
tail_lines = 200

[[connections]]
id = "lab"
kind = "ssh"
ssh_host = "lab-server"
[connections.logs]
poll_interval = "30s"
```

Task/watch overrides take precedence over connection settings, then global settings. Watch mode, interval, line count and collection pause are saved in UI state. Commands, environment values and log content are not saved there.

Only visible log tiles collect automatically; hidden Monitor pages stop their log reads. Queue status refresh is independent. Due requests with the same connection and tail size are batched, with one polling request per connection and at most four concurrent polling workers. Slow requests do not build a catch-up queue. Progress probes for other visible running rows use a smaller 20-line tail at intervals of at least 10 seconds.

Each primary log buffer retains at most 10,000 lines / 2 MiB, including stored styles. Cold sessions are evicted under a 32 MiB log-cache budget, with at most 64 cold session records. These are application content bounds, not a process-RSS guarantee. Pueue's tail API is line-based, so one unusually large line can still cause additional upstream I/O before application truncation.

Polling gives control over cadence and reduces process/SSH call frequency through batching. It retransmits each requested tail, while an idle native follow stream may transmit nothing. Polling is therefore not inherently cheaper in bytes than Live; choose a longer interval and smaller tail for slowly changing jobs.
