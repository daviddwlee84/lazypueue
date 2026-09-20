package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/maintenance"
	"github.com/daviddwlee84/lazypueue/internal/selfupdate"
	"github.com/daviddwlee84/lazypueue/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Options supplies dependencies without requiring a daemon or terminal in tests.
type Options struct {
	SelfCheck    func(context.Context) (selfupdate.Plan, error)
	SelfApply    func(context.Context, selfupdate.Plan, selfupdate.ApplyOptions, io.Writer) (selfupdate.Result, error)
	Maintenance  *maintenance.Service
	Backend      core.Backend
	Input        io.Reader
	Output       io.Writer
	ErrorOutput  io.Writer
	IsTerminal   func() bool
	RunDashboard func(context.Context, config.Config, string, core.Backend, string) error
	Version      string
}

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string           { return e.Err.Error() }
func (e *ExitError) Unwrap() error           { return e.Err }
func usage(format string, args ...any) error { return &ExitError{2, fmt.Errorf(format, args...)} }

// ExitCode translates command failures into the documented process status.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	if strings.HasPrefix(err.Error(), "unknown command ") {
		return 2
	}
	var e *ExitError
	if errors.As(err, &e) {
		return e.Code
	}
	return 1
}

// PrintError keeps machine-readable errors on stderr. Command handlers return
// errors; the entry point prints exactly once after terminal cleanup.
func PrintError(w io.Writer, err error, jsonMode bool) {
	if err == nil {
		return
	}
	if jsonMode {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "exit_code": ExitCode(err)})
		return
	}
	fmt.Fprintln(w, "lazypueue:", err)
}

// Execute uses Cobra's parsed flag state: a literal --json after --, or used as
// a command-field value, cannot accidentally change error formatting.
func Execute(ctx context.Context, options Options, args []string) int {
	root := NewRoot(options)
	root.SetArgs(append([]string{}, args...))
	err := root.ExecuteContext(ctx)
	jsonMode, _ := root.PersistentFlags().GetBool("json")
	PrintError(root.ErrOrStderr(), err, jsonMode)
	return ExitCode(err)
}

type app struct {
	options                        Options
	root                           *cobra.Command
	configPath, connection         string
	json, interactive, yes, dryRun bool
}

func NewRoot(options Options) *cobra.Command {
	if options.Input == nil {
		options.Input = os.Stdin
	}
	if options.Output == nil {
		options.Output = os.Stdout
	}
	if options.ErrorOutput == nil {
		options.ErrorOutput = os.Stderr
	}
	if options.IsTerminal == nil {
		options.IsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) }
	}
	if options.RunDashboard == nil {
		options.RunDashboard = tui.Run
	}
	if options.Version == "" {
		options.Version = "dev"
	}
	a := &app{options: options}
	root := &cobra.Command{
		Use: "lazypueue", Short: "A keyboard-driven dashboard for local and remote Pueue queues", Version: options.Version,
		SilenceErrors: true, SilenceUsage: true,
		Args: noArgs,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if a.interactive && a.json {
				return usage("--interactive cannot be combined with --json")
			}
			if a.interactive && !a.options.IsTerminal() {
				return usage("--interactive requires an input and output terminal")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.json {
				return usage("--json requires a data command, for example: lazypueue status --json")
			}
			if a.dryRun {
				return usage("--dry-run requires a mutation command, for example: lazypueue add --dry-run -- 'echo hello'")
			}
			if !a.options.IsTerminal() {
				return cmd.Help()
			}
			cfg, path, err := a.load()
			if err != nil {
				return err
			}
			return a.options.RunDashboard(cmd.Context(), cfg, path, a.options.Backend, a.connection)
		},
	}
	a.root = root
	root.SetVersionTemplate("{{if .Flags.GetBool \"json\"}}{\"version\":{{printf \"%q\" .Version}}}{{else}}{{.Name}} version {{.Version}}{{end}}\n")
	root.SetIn(options.Input)
	root.SetOut(options.Output)
	root.SetErr(options.ErrorOutput)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error { return usage("%v; see %s --help", err, cmd.CommandPath()) })
	f := root.PersistentFlags()
	f.StringVar(&a.configPath, "config", "", "Configuration file (default: $XDG_CONFIG_HOME/lazypueue/config.toml)")
	f.StringVarP(&a.connection, "connection", "c", "", "Connection ID; 'all' is read-only")
	f.BoolVar(&a.json, "json", false, "Write structured JSON without prompts")
	f.BoolVar(&a.interactive, "interactive", false, "Open a supported wizard with supplied flags as defaults")
	f.BoolVarP(&a.yes, "yes", "y", false, "Approve the operation's confirmation")
	f.BoolVar(&a.dryRun, "dry-run", false, "Preview without applying changes (edit/upgrade previews perform read-only checks)")
	_ = root.RegisterFlagCompletionFunc("connection", func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		cfg, err := config.Load(a.configPath)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		values := []string{"all"}
		for _, c := range cfg.Connections {
			values = append(values, c.ID)
		}
		return values, cobra.ShellCompDirectiveNoFileComp
	})
	root.AddCommand(a.statusCommand(), a.logCommand(false), a.logCommand(true), a.addCommand())
	for _, op := range []string{"pause", "start", "restart", "stash", "enqueue", "kill", "remove"} {
		root.AddCommand(a.taskCommand(op))
	}
	root.AddCommand(a.cleanCommand(), a.restartFailedCommand(), a.groupCommand(), a.parallelCommand(), a.connectionsCommand(), a.configCommand())
	root.AddCommand(a.upgradeCommand(), a.backendCommand(), a.editCommand())
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print the build version", Args: noArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if a.json {
			return a.writeJSON(cmd, map[string]string{"version": options.Version})
		}
		fmt.Fprintln(cmd.OutOrStdout(), options.Version)
		return nil
	}})
	root.AddCommand(a.completionCommand())
	return root
}

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usage("%s does not accept arguments; see %s --help", cmd.CommandPath(), cmd.CommandPath())
	}
	return nil
}

func choices(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usage("%s expects %d argument(s); see %s --help", cmd.CommandPath(), n, cmd.CommandPath())
		}
		return nil
	}
}
func parseIDs(args []string) ([]int, error) {
	if len(args) == 0 {
		return nil, usage("provide at least one task ID")
	}
	seen := map[int]bool{}
	ids := make([]int, 0, len(args))
	for _, arg := range args {
		id, err := strconv.Atoi(arg)
		if err != nil || id < 0 {
			return nil, usage("invalid task ID %q (expected a non-negative integer)", arg)
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, nil
}
func (a *app) load() (config.Config, string, error) {
	path, err := config.ResolvePath(a.configPath)
	if err != nil {
		return config.Config{}, "", usage("configuration: %v", err)
	}
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return cfg, path, usage("configuration: %v (repair with lazypueue --config %q config edit)", err, path)
	}
	return cfg, path, nil
}
func (a *app) target(cfg config.Config, write bool) (core.Connection, error) {
	if a.connection == "all" {
		return core.Connection{}, usage("this operation requires one connection; use --connection ID")
	}
	c, err := cfg.Connection(a.connection)
	if err != nil {
		return c, usage("%v", err)
	}
	return c, nil
}
func (a *app) writeJSON(cmd *cobra.Command, value any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
func (a *app) commandOnly() error {
	if a.interactive {
		return usage("--interactive is supported by add, connections add/edit, and the dashboard")
	}
	return nil
}
func (a *app) groupHelp(cmd *cobra.Command, args []string) error {
	if a.json {
		return usage("choose a subcommand; see %s --help", cmd.CommandPath())
	}
	if a.interactive {
		return usage("choose a wizard-capable subcommand; see %s --help", cmd.CommandPath())
	}
	return cmd.Help()
}

func (a *app) confirm(cmd *cobra.Command, message string) error {
	if a.yes {
		return nil
	}
	if a.json || !a.options.IsTerminal() {
		return usage("%s; pass --yes to confirm", message)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", message)
	type answer struct {
		line string
		err  error
	}
	answers := make(chan answer, 1)
	go func() { line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n'); answers <- answer{line, err} }()
	var line string
	var err error
	select {
	case <-cmd.Context().Done():
		return cmd.Context().Err()
	case value := <-answers:
		line, err = value.line, value.err
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes") {
		return nil
	}
	return &ExitError{130, errors.New("cancelled; no operation was submitted")}
}

func (a *app) mutate(cmd *cobra.Command, c core.Connection, request core.Request, confirmation string) error {
	plan, err := a.options.Backend.Preview(c, request)
	if err != nil {
		return usage("%v", err)
	}
	if a.dryRun {
		if a.json {
			return a.writeJSON(cmd, plan)
		}
		fmt.Fprintln(cmd.OutOrStdout(), plan.Display)
		for _, s := range plan.Consequences {
			fmt.Fprintln(cmd.OutOrStdout(), s)
		}
		return nil
	}
	if confirmation != "" {
		if !a.yes && !a.json && a.options.IsTerminal() {
			var tasks []core.Task
			var reviewed bool
			request, tasks, reviewed, err = a.reviewRequest(cmd, c, request)
			if err != nil {
				return err
			}
			if reviewed && len(tasks) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No matching tasks on %s; nothing was submitted.\n", c.DisplayName())
				return nil
			}
			for _, t := range tasks {
				description := t.Label
				if description == "" {
					description = t.Command
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "  #%d [%s] %s\n", t.ID, oneLine(t.State()), oneLine(description))
			}
		}
		if err := a.confirm(cmd, fmt.Sprintf("%s on %s", confirmation, c.DisplayName())); err != nil {
			return err
		}
	}
	result, err := a.options.Backend.Execute(cmd.Context(), c, request)
	if a.json {
		if result.ConnectionID != "" || err == nil {
			if writeErr := a.writeJSON(cmd, result); writeErr != nil {
				return writeErr
			}
		}
	} else if result.Message != "" {
		fmt.Fprintln(cmd.OutOrStdout(), result.Message)
	}
	return err
}

// Freeze reviewed IDs and their creation timestamps before waiting for input.
// The backend rechecks them immediately before mutation. A zero-match batch
// returns without Execute, whose empty-ID contract otherwise means discovery.
func (a *app) reviewRequest(cmd *cobra.Command, c core.Connection, request core.Request) (core.Request, []core.Task, bool, error) {
	batch := request.Operation == "clean" || request.Operation == "restart-failed"
	if len(request.IDs) == 0 && !batch {
		return request, nil, false, nil
	}
	snapshot, err := a.options.Backend.Snapshot(cmd.Context(), c)
	if err != nil {
		return request, nil, true, err
	}
	expected := request.Guards
	request.Guards = map[int]time.Time{}
	for id, stamp := range expected {
		found := false
		for _, task := range snapshot.Tasks {
			if task.ID == id && task.CreatedAt.Equal(stamp) {
				found = true
				break
			}
		}
		if !found {
			return request, nil, true, fmt.Errorf("task #%d changed since the draft was prepared; select it again", id)
		}
		request.Guards[id] = stamp
	}
	var tasks []core.Task
	if batch && len(request.IDs) == 0 {
		for _, t := range snapshot.Tasks {
			if request.Group != "" && t.Group != request.Group {
				continue
			}
			if !core.Eligible(request.Operation, t) {
				continue
			}
			if request.Operation == "clean" && request.SuccessfulOnly && t.State() != "succeeded" {
				continue
			}
			if request.Operation == "restart-failed" && !t.Failed() {
				continue
			}
			request.IDs = append(request.IDs, t.ID)
			request.Guards[t.ID] = t.CreatedAt
			tasks = append(tasks, t)
		}
	} else {
		byID := map[int]core.Task{}
		for _, t := range snapshot.Tasks {
			byID[t.ID] = t
		}
		for _, id := range request.IDs {
			t, ok := byID[id]
			if !ok {
				return request, nil, true, fmt.Errorf("task #%d no longer exists on %s", id, c.DisplayName())
			}
			if !core.Eligible(request.Operation, t) {
				return request, nil, true, fmt.Errorf("task #%d (%s) is not eligible for %s", id, t.State(), request.Operation)
			}
			request.Guards[id] = t.CreatedAt
			tasks = append(tasks, t)
		}
	}
	return request, tasks, true, nil
}

func (a *app) completionCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "completion bash|zsh|fish|powershell", Short: "Generate shell completion", Args: exactArgs(1), ValidArgs: []string{"bash", "zsh", "fish", "powershell"}}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if a.json || a.interactive {
			return usage("completion does not support --json or --interactive")
		}
		switch args[0] {
		case "bash":
			return a.root.GenBashCompletionV2(cmd.OutOrStdout(), true)
		case "zsh":
			return a.root.GenZshCompletion(cmd.OutOrStdout())
		case "fish":
			return a.root.GenFishCompletion(cmd.OutOrStdout(), true)
		case "powershell":
			return a.root.GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
		default:
			return usage("unsupported shell %q", args[0])
		}
	}
	return cmd
}
