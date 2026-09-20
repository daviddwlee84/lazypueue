package cli

import (
	"fmt"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/form"
	"github.com/spf13/cobra"
)

func (a *app) editCommand() *cobra.Command {
	var draft core.EditRequest
	var inPlace bool
	cmd := &cobra.Command{Use: "edit ID", Short: "Review and edit one task; retry finished tasks or stop and retry a running task", Args: exactArgs(1)}
	cmd.Flags().StringVar(&draft.Command, "command", "", "Replacement shell command")
	cmd.Flags().StringVarP(&draft.Directory, "directory", "w", "", "Replacement working directory")
	cmd.Flags().StringVarP(&draft.Label, "label", "l", "", "Replacement label (empty clears it)")
	cmd.Flags().IntVarP(&draft.Priority, "priority", "o", 0, "Replacement priority")
	cmd.Flags().BoolVar(&draft.Stashed, "stashed", false, "Keep the edited task stashed")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "Reuse a finished/running task's ID on retry (overwrites its log)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ids, err := parseIDs(args)
		if err != nil {
			return err
		}
		return a.editSelected(cmd, ids[0], "", inPlace, &draft, false)
	}
	return cmd
}

func (a *app) editSelected(cmd *cobra.Command, id int, operation string, inPlace bool, draft *core.EditRequest, forceWizard bool) error {
	changed := false
	for _, name := range []string{"command", "directory", "label", "priority", "stashed"} {
		if cmd.Flags().Lookup(name) != nil && cmd.Flags().Changed(name) {
			changed = true
		}
	}
	wizard := forceWizard || a.interactive || !changed
	if wizard && (a.json || a.dryRun || !a.options.IsTerminal()) {
		return usage("interactive edit needs a terminal and cannot use --json/--dry-run; use edit ID --command ... --yes for scripts")
	}
	cfg, _, err := a.load()
	if err != nil {
		return err
	}
	conn, err := a.target(cfg, true)
	if err != nil {
		return err
	}
	snapshot, err := a.options.Backend.Snapshot(cmd.Context(), conn)
	if err != nil {
		return err
	}
	var task core.Task
	found := false
	for _, t := range snapshot.Tasks {
		if t.ID == id {
			task = t
			found = true
		}
	}
	if !found {
		return fmt.Errorf("task #%d no longer exists", id)
	}
	if task.Locked {
		return fmt.Errorf("task #%d is locked; finish its editor or use the recovery action", id)
	}
	if operation == "" {
		switch task.State() {
		case "running", "paused":
			operation = "stop-edit-restart"
		case "succeeded", "failed":
			operation = "edit-restart"
		default:
			operation = "edit"
		}
	}
	if !core.Eligible(operation, task) {
		return usage("task #%d is not eligible for %s", id, operation)
	}
	command := task.OriginalCommand
	if command == "" {
		command = task.Command
	}
	edit := core.EditRequest{Command: command, Directory: task.Path, Label: task.Label, Priority: task.Priority, Stashed: task.State() == "stashed" && task.ScheduledAt == nil}
	if draft != nil {
		if cmd.Flags().Changed("command") {
			edit.Command = draft.Command
		}
		if cmd.Flags().Changed("directory") {
			edit.Directory = draft.Directory
		}
		if cmd.Flags().Changed("label") {
			edit.Label = draft.Label
		}
		if cmd.Flags().Changed("priority") {
			edit.Priority = draft.Priority
		}
		if cmd.Flags().Changed("stashed") {
			edit.Stashed = draft.Stashed
		}
	}
	request := core.Request{Operation: operation, IDs: []int{id}, Guards: map[int]time.Time{id: task.CreatedAt}, Edit: &edit, InPlace: inPlace}
	if wizard {
		model := form.NewEdit(task, conn, a.options.Backend)
		model.SetRequest(request)
		result, err := a.runEditWizard(cmd, conn, model)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), result.Message)
		return nil
	}
	return a.mutate(cmd, conn, request, fmt.Sprintf("Apply edit to task #%d (%s)\n  Command: %s\n  Directory: %s\n  Label: %s · Priority: %d · Stashed: %t\nQueued work may start and retries may consume resources", id, operation, oneLine(edit.Command), oneLine(edit.Directory), oneLine(edit.Label), edit.Priority, edit.Stashed))
}

func (a *app) clearGroupCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "clear NAME", Short: "Stop and remove the reviewed tasks and logs; keep the group", Args: exactArgs(1)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		conn, err := a.target(cfg, true)
		if err != nil {
			return err
		}
		request := core.Request{Operation: "clear-group", Group: args[0]}
		if !a.dryRun {
			snapshot, err := a.options.Backend.Snapshot(cmd.Context(), conn)
			if err != nil {
				return err
			}
			groupExists := false
			for _, g := range snapshot.Groups {
				if g.Name == request.Group {
					groupExists = true
				}
			}
			if !groupExists {
				return fmt.Errorf("group %q does not exist", request.Group)
			}
			request.IDs = []int{}
			request.Guards = map[int]time.Time{}
			for _, t := range snapshot.Tasks {
				if t.Group == request.Group {
					request.IDs = append(request.IDs, t.ID)
					request.Guards[t.ID] = t.CreatedAt
				}
			}
			if len(request.IDs) == 0 {
				result := core.Result{ConnectionID: conn.ID, Message: "No tasks to clear"}
				if a.json {
					return a.writeJSON(cmd, result)
				}
				fmt.Fprintln(cmd.OutOrStdout(), result.Message)
				return nil
			}
		}
		return a.mutate(cmd, conn, request, fmt.Sprintf("Stop and remove the reviewed tasks %v and their logs from group %s; keep the group", request.IDs, request.Group))
	}
	return cmd
}
