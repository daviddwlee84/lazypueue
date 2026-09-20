package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/form"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func (a *app) addCommand() *cobra.Command {
	draft := core.AddRequest{Group: "default", Mode: "queued", GroupParallel: 1}
	var after []string
	var stashed, immediate bool
	cmd := &cobra.Command{Use: "add [flags] -- 'COMMAND'", Short: "Add a task, or open a task wizard when called bare", Example: "  lazypueue add\n  lazypueue add --label train-seed1 -- python\n  lazypueue -c lab add -w /srv/project -g ml -- 'python train.py --seed 1'"}
	f := cmd.Flags()
	f.StringVar(&draft.Command, "command", "", "Complete shell command (alternative to one positional shell string)")
	f.StringVarP(&draft.Directory, "directory", "w", "", "Working directory on the daemon host (local defaults to current directory)")
	f.StringVarP(&draft.Group, "group", "g", "default", "Queue group")
	f.StringVarP(&draft.Label, "label", "l", "", "Task label")
	f.StringVar(&draft.Mode, "mode", "queued", "Start mode: queued, stashed, immediate")
	f.BoolVar(&stashed, "stashed", false, "Stash instead of queueing")
	f.BoolVar(&immediate, "immediate", false, "Start immediately, bypassing dependency and parallel limits")
	f.StringSliceVarP(&after, "after", "a", nil, "Dependency IDs (comma-separated or repeated)")
	f.StringVar(&draft.Delay, "delay", "", "Pueue delay expression, for example 30m or 2026-09-21 09:00")
	f.IntVarP(&draft.Priority, "priority", "o", 0, "Scheduling priority")
	f.BoolVar(&draft.CreateGroup, "create-group", false, "Create the chosen group if missing")
	f.IntVar(&draft.GroupParallel, "group-parallel", 1, "Concurrency for a newly created group (0 unlimited)")
	_ = cmd.RegisterFlagCompletionFunc("mode", choices("queued", "stashed", "immediate"))
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 1 {
			return usage("pass the complete shell command as one quoted argument, for example: lazypueue add -- 'python train.py --seed 1'")
		}
		if len(args) > 0 && cmd.Flags().Changed("command") {
			return usage("use --command or a positional command, not both")
		}
		if len(args) > 0 {
			draft.Command = args[0]
		}
		if stashed && immediate {
			return usage("--stashed and --immediate are mutually exclusive")
		}
		if (stashed || immediate) && cmd.Flags().Changed("mode") {
			return usage("use --mode or --stashed/--immediate, not both")
		}
		if stashed {
			draft.Mode = "stashed"
		}
		if immediate {
			draft.Mode = "immediate"
		}
		for _, raw := range after {
			id, err := strconv.Atoi(raw)
			if err != nil || id < 0 {
				return usage("invalid dependency ID %q", raw)
			}
			draft.After = append(draft.After, id)
		}
		if err := core.ValidateAddPartial(draft); err != nil {
			return usage("%v", err)
		}
		bare := len(args) == 0 && !changedBusinessFlags(cmd)
		wizard := a.interactive || (bare && !a.json && !a.dryRun && a.options.IsTerminal())
		if a.interactive && a.dryRun {
			return usage("--interactive cannot be combined with --dry-run; provide fields to preview without discovery")
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		if a.connection == "all" && !wizard {
			return usage("add requires one connection; use --connection ID")
		}
		c, err := cfg.Connection(a.connection)
		if err != nil {
			return usage("%v", err)
		}
		if wizard {
			if state, e := config.LoadState(); e == nil {
				if last, ok := state.LastUsed[c.ID]; ok {
					if !cmd.Flags().Changed("directory") {
						draft.Directory = last.Directory
					}
					if !cmd.Flags().Changed("group") {
						draft.Group = last.Group
					}
				}
			}
			model := form.NewTask(cfg.Connections, draft, c.ID, a.options.Backend)
			result, request, target, err := a.runTaskWizard(cmd, cfg, model)
			if err != nil {
				return err
			}
			a.remember(cmd, target, request)
			fmt.Fprintln(cmd.OutOrStdout(), result.Message)
			return nil
		}
		if draft.Directory == "" && c.Kind == "local" {
			draft.Directory, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		if err := core.ValidateAdd(draft); err != nil {
			return usage("%v; example: lazypueue add -w /path -- 'command'; see lazypueue add --help", err)
		}
		confirmation := ""
		if draft.Mode == "immediate" {
			confirmation = "Start a new task immediately, bypassing dependency and parallel limits"
		}
		err = a.mutate(cmd, c, core.Request{Operation: "add", Add: &draft}, confirmation)
		if err == nil && !a.dryRun {
			a.remember(cmd, c.ID, draft)
		}
		return err
	}
	return cmd
}

func changedBusinessFlags(cmd *cobra.Command) bool {
	changed := false
	cmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Changed && f.Name != "help" {
			changed = true
		}
	})
	return changed
}

func (a *app) remember(cmd *cobra.Command, connection string, request core.AddRequest) {
	state, err := config.LoadState()
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Task submitted, but recent defaults could not be read: %v\n", err)
		return
	}
	state.LastUsed[connection] = config.LastUsed{Directory: request.Directory, Group: request.Group}
	if err := config.SaveState(state); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Task submitted, but recent defaults could not be saved: %v\n", err)
	}
}

func parseEditor(command string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range command {
		if escaped {
			word.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			started = true
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			started = true
		case ' ', '\t', '\n':
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unclosed quote or escape in editor command")
	}
	if started {
		words = append(words, word.String())
	}
	if len(words) == 0 || words[0] == "" {
		return nil, fmt.Errorf("empty editor command")
	}
	return words, nil
}
