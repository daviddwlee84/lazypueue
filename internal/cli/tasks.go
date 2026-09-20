package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/spf13/cobra"
)

type connectionStatus struct {
	Connection string         `json:"connection"`
	Snapshot   *core.Snapshot `json:"snapshot,omitempty"`
	Error      string         `json:"error,omitempty"`
}

func (a *app) snapshots(cmd *cobra.Command, cfg config.Config) ([]connectionStatus, bool, error) {
	connections := cfg.Connections
	all := a.connection == "all" || (a.connection == "" && cfg.DefaultConnection == "all")
	if !all {
		c, err := a.target(cfg, false)
		if err != nil {
			return nil, all, err
		}
		connections = []core.Connection{c}
	}
	rows := make([]connectionStatus, len(connections))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, c := range connections {
		wg.Add(1)
		go func(i int, c core.Connection) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			snap, err := a.options.Backend.Snapshot(cmd.Context(), c)
			rows[i].Connection = c.ID
			if err != nil {
				rows[i].Error = err.Error()
				return
			}
			rows[i].Snapshot = &snap
		}(i, c)
	}
	wg.Wait()
	return rows, all, nil
}

func snapshotErrors(rows []connectionStatus) error {
	failures := 0
	for _, row := range rows {
		if row.Error != "" {
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d connection(s) could not be read", failures, len(rows))
	}
	return nil
}

func (a *app) statusCommand() *cobra.Command {
	var group, state, search string
	cmd := &cobra.Command{Use: "status", Short: "Show queue tasks and group capacity", Args: noArgs}
	cmd.Flags().StringVarP(&group, "group", "g", "", "Filter by group")
	cmd.Flags().StringVar(&state, "state", "", "Filter by task status or result")
	cmd.Flags().StringVarP(&search, "search", "s", "", "Search command, label, and directory")
	_ = cmd.RegisterFlagCompletionFunc("state", choices("running", "queued", "paused", "stashed", "locked", "succeeded", "failed"))
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		if a.dryRun {
			return usage("status is read-only; --dry-run is for mutation commands")
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		rows, all, err := a.snapshots(cmd, cfg)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Snapshot != nil {
				snap := row.Snapshot
				snap.Tasks = filterTasks(snap.Tasks, group, state, search)
				if group != "" {
					groups := []core.Group{}
					for _, g := range snap.Groups {
						if g.Name == group {
							groups = append(groups, g)
						}
					}
					snap.Groups = groups
				}
			}
		}
		if a.json {
			if !all && len(rows) == 1 && rows[0].Snapshot != nil {
				err = a.writeJSON(cmd, rows[0].Snapshot)
			} else {
				err = a.writeJSON(cmd, map[string]any{"connections": rows})
			}
			if err != nil {
				return err
			}
		} else {
			for i, row := range rows {
				if i > 0 {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				if row.Error != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", row.Connection, row.Error)
					continue
				}
				a.printSnapshot(cmd, *row.Snapshot)
			}
		}
		return snapshotErrors(rows)
	}
	return cmd
}
func filterTasks(tasks []core.Task, group, state, search string) []core.Task {
	out := make([]core.Task, 0, len(tasks))
	search = strings.ToLower(search)
	for _, t := range tasks {
		if group != "" && t.Group != group {
			continue
		}
		if state != "" && !strings.EqualFold(t.Status, state) && !strings.EqualFold(t.Result, state) && !strings.EqualFold(t.State(), state) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(t.Command+" "+t.Label+" "+t.Path), search) {
			continue
		}
		out = append(out, t)
	}
	return out
}
func (a *app) printSnapshot(cmd *cobra.Command, s core.Snapshot) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s · %d tasks\n", s.ConnectionID, len(s.Tasks))
	for _, g := range s.Groups {
		parallel := strconv.Itoa(g.Parallel)
		if g.Parallel == 0 {
			parallel = "unlimited"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s · parallel %s\n", g.Name, g.Status, parallel)
	}
	if len(s.Tasks) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No matching tasks.")
		return
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tGROUP\tLABEL\tCOMMAND")
	for _, t := range s.Tasks {
		state := t.Status
		if t.Result != "" {
			state = t.Result
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", t.ID, state, oneLine(t.Group), oneLine(t.Label), oneLine(t.Command))
	}
	_ = w.Flush()
}
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(s))
	return strings.NewReplacer("\n", " ↵ ", "\t", " ").Replace(s)
}

func (a *app) logCommand(follow bool) *cobra.Command {
	var lines int
	var full bool
	name, short := "log", "Read a bounded task log"
	if follow {
		name = "follow"
		short = "Follow a task's output until it ends or Ctrl+C"
	}
	cmd := &cobra.Command{Use: name + " ID [ID...]", Short: short}
	if follow {
		cmd.Args = exactArgs(1)
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 200, "Number of initial tail lines")
	if !follow {
		cmd.Flags().BoolVar(&full, "full", false, "Stream complete plain-text logs (cannot use --json)")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ids, err := parseIDs(args)
		if err != nil {
			return err
		}
		if full && (a.json || cmd.Flags().Changed("lines")) {
			return usage("--full cannot be combined with --json or --lines")
		}
		if lines < 1 {
			return usage("--lines must be positive")
		}
		if err = a.commandOnly(); err != nil {
			return err
		}
		if a.dryRun {
			return usage("%s is read-only; --dry-run is for mutation commands", name)
		}
		if follow && a.json {
			return usage("follow streams text and cannot be combined with --json; use log --json")
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		c, err := a.target(cfg, false)
		if err != nil {
			return err
		}
		if follow {
			return a.options.Backend.Follow(cmd.Context(), c, ids[0], lines, func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) })
		}
		if full {
			streamer, ok := a.options.Backend.(interface {
				FullLog(context.Context, core.Connection, int, io.Writer) error
			})
			if !ok {
				return fmt.Errorf("backend does not support full log streaming")
			}
			for _, id := range ids {
				if len(ids) > 1 {
					fmt.Fprintf(cmd.OutOrStdout(), "== %s #%d ==\n", c.ID, id)
				}
				if err := streamer.FullLog(cmd.Context(), c, id, cmd.OutOrStdout()); err != nil {
					return err
				}
			}
			return nil
		}
		entries := map[int]core.LogResult{}
		if batch, ok := a.options.Backend.(core.BatchLogger); ok {
			entries, err = batch.Logs(cmd.Context(), c, ids, lines)
		} else {
			for _, id := range ids {
				text, e := a.options.Backend.Log(cmd.Context(), c, id, lines)
				item := core.LogResult{Output: text}
				if e != nil {
					item.Error = e.Error()
				}
				entries[id] = item
			}
		}
		if err != nil {
			return err
		}
		var logError error
		for _, id := range ids {
			if entries[id].Error != "" {
				logError = fmt.Errorf("one or more task logs are unavailable")
			}
		}
		if a.json {
			if len(ids) == 1 {
				item := entries[ids[0]]
				if err := a.writeJSON(cmd, map[string]any{"connection": c.ID, "task_id": ids[0], "output": item.Output, "error": item.Error}); err != nil {
					return err
				}
			} else {
				if err := a.writeJSON(cmd, map[string]any{"connection": c.ID, "logs": entries}); err != nil {
					return err
				}
			}
		} else {
			for _, id := range ids {
				if len(ids) > 1 {
					fmt.Fprintf(cmd.OutOrStdout(), "== %s #%d ==\n", c.ID, id)
				}
				item := entries[id]
				if item.Error != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "#%d: %s\n", id, item.Error)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), item.Output)
				}
			}
		}
		return logError
	}
	return cmd
}

func (a *app) taskCommand(operation string) *cobra.Command {
	var inPlace, edit bool
	cmd := &cobra.Command{Use: operation + " ID [ID...]", Short: map[string]string{"pause": "Pause running tasks", "start": "Force-start or resume tasks", "restart": "Restart tasks as new tasks; retain old logs", "stash": "Stash queued tasks", "enqueue": "Enqueue stashed tasks", "kill": "Kill running tasks", "remove": "Remove tasks and their logs"}[operation]}
	if operation == "restart" {
		cmd.Flags().BoolVarP(&inPlace, "in-place", "i", false, "Reuse task IDs and overwrite their logs (requires confirmation)")
		cmd.Flags().BoolVarP(&edit, "edit", "e", false, "Edit one finished task before retrying (interactive form)")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ids, err := parseIDs(args)
		if err != nil {
			return err
		}
		if operation == "restart" && edit {
			if len(ids) != 1 {
				return usage("restart --edit accepts exactly one task")
			}
			return a.editSelected(cmd, ids[0], "edit-restart", inPlace, nil, true)
		}
		if err = a.commandOnly(); err != nil {
			return err
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		c, err := a.target(cfg, true)
		if err != nil {
			return err
		}
		confirmation := ""
		switch operation {
		case "kill":
			confirmation = fmt.Sprintf("Kill task(s) %v and stop their processes", ids)
		case "remove":
			confirmation = fmt.Sprintf("Remove task(s) %v and their logs", ids)
		case "start":
			confirmation = fmt.Sprintf("Start task(s) %v immediately; queued/stashed tasks may bypass dependencies and parallel limits", ids)
		case "restart":
			confirmation = fmt.Sprintf("Restart task(s) %v as new tasks; preserve original logs and clear dependencies", ids)
			if inPlace {
				confirmation = fmt.Sprintf("Restart task(s) %v in place and overwrite their logs", ids)
			}
		}
		return a.mutate(cmd, c, core.Request{Operation: operation, IDs: ids, InPlace: inPlace}, confirmation)
	}
	return cmd
}

func (a *app) cleanCommand() *cobra.Command {
	var group string
	var successfulOnly bool
	cmd := &cobra.Command{Use: "clean", Short: "Remove finished tasks and logs (successful tasks by default)", Args: noArgs}
	cmd.Flags().StringVarP(&group, "group", "g", "", "Limit cleanup to a group")
	cmd.Flags().BoolVar(&successfulOnly, "successful-only", true, "Keep failed tasks; set false to remove all finished tasks")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		c, err := a.target(cfg, true)
		if err != nil {
			return err
		}
		target := "all groups"
		if group != "" {
			target = "group " + group
		}
		kind := "successful"
		if !successfulOnly {
			kind = "all finished (including failed)"
		}
		return a.mutate(cmd, c, core.Request{Operation: "clean", Group: group, SuccessfulOnly: successfulOnly}, "Delete "+kind+" tasks and their logs in "+target)
	}
	return cmd
}

func (a *app) groupCommand() *cobra.Command {
	parent := &cobra.Command{Use: "group", Short: "Manage queue groups", Args: noArgs, RunE: a.groupHelp}
	list := &cobra.Command{Use: "list", Short: "List groups", Args: noArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		if a.dryRun {
			return usage("group list is read-only; --dry-run is for mutation commands")
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		rows, all, err := a.snapshots(cmd, cfg)
		if err != nil {
			return err
		}
		if a.json {
			values := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				value := map[string]any{"connection": row.Connection}
				if row.Snapshot != nil {
					value["groups"] = row.Snapshot.Groups
				} else {
					value["error"] = row.Error
				}
				values = append(values, value)
			}
			var value any = map[string]any{"connections": values}
			if !all && len(values) == 1 {
				value = values[0]
			}
			if err := a.writeJSON(cmd, value); err != nil {
				return err
			}
		} else {
			for _, row := range rows {
				if row.Error != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", row.Connection, row.Error)
					continue
				}
				for _, g := range row.Snapshot.Groups {
					slots := strconv.Itoa(g.Parallel)
					if g.Parallel == 0 {
						slots = "unlimited"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", row.Connection, g.Name, g.Status, slots)
				}
			}
		}
		return snapshotErrors(rows)
	}}
	parent.AddCommand(list, a.clearGroupCommand())
	for _, op := range []string{"add", "remove", "pause", "start"} {
		operation := op
		cmd := &cobra.Command{Use: op + " NAME", Short: strings.ToUpper(op[:1]) + op[1:] + " a group", Args: exactArgs(1)}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if err := a.commandOnly(); err != nil {
				return err
			}
			cfg, _, err := a.load()
			if err != nil {
				return err
			}
			c, err := a.target(cfg, true)
			if err != nil {
				return err
			}
			confirmation := ""
			if operation != "add" {
				confirmation = strings.ToUpper(operation[:1]) + operation[1:] + " group " + args[0]
				if operation == "remove" {
					confirmation = "Remove empty group " + args[0]
				}
			}
			return a.mutate(cmd, c, core.Request{Operation: "group-" + operation, Group: args[0], Parallel: 1}, confirmation)
		}
		parent.AddCommand(cmd)
	}
	return parent
}

func (a *app) restartFailedCommand() *cobra.Command {
	var group string
	var inPlace bool
	cmd := &cobra.Command{Use: "restart-failed", Short: "Restart failed tasks in one connection", Args: noArgs}
	cmd.Flags().StringVarP(&group, "group", "g", "", "Restrict to one group")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "Reuse task IDs and overwrite their logs")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		c, err := a.target(cfg, true)
		if err != nil {
			return err
		}
		confirmation := "Restart failed tasks as new tasks"
		if inPlace {
			confirmation = "Restart failed tasks in place and overwrite their logs"
		}
		if group != "" {
			confirmation += " in group " + group
		}
		return a.mutate(cmd, c, core.Request{Operation: "restart-failed", Group: group, InPlace: inPlace}, confirmation)
	}
	return cmd
}

func (a *app) parallelCommand() *cobra.Command {
	var group string
	cmd := &cobra.Command{Use: "parallel COUNT", Short: "Set group concurrency (0 means unlimited)", Args: exactArgs(1)}
	cmd.Flags().StringVarP(&group, "group", "g", "default", "Group to update")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 {
			return usage("COUNT must be a non-negative integer (0 means unlimited)")
		}
		if err = a.commandOnly(); err != nil {
			return err
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		c, err := a.target(cfg, true)
		if err != nil {
			return err
		}
		return a.mutate(cmd, c, core.Request{Operation: "parallel", Group: group, Parallel: n}, fmt.Sprintf("Set group %s parallelism to %d", group, n))
	}
	return cmd
}
