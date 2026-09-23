package cli

import (
	"fmt"
	"github.com/daviddwlee84/lazypueue/internal/scoopupgrade"
	"io"
	"strings"

	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/maintenance"
	"github.com/daviddwlee84/lazypueue/internal/selfupdate"
	"github.com/spf13/cobra"
)

func (a *app) upgradeCommand() *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{Use: "upgrade", Short: "Check or upgrade lazypueue through its verified installation method", Args: noArgs}
	cmd.Flags().BoolVar(&check, "check", false, "Inspect the running binary and upgrade method without changing files")
	cmd.Flags().BoolVar(&force, "force", false, "Explicitly replace a recognized development build (never bypass package ownership)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		checker := a.options.SelfCheck
		if checker == nil {
			checker = selfupdate.Check
		}
		apply := a.options.SelfApply
		if apply == nil {
			apply = selfupdate.Apply
		}
		plan, err := checker(cmd.Context())
		if err != nil {
			return err
		}
		if check || a.dryRun {
			if a.json {
				return a.writeJSON(cmd, plan)
			}
			return printSelfPlan(cmd.OutOrStdout(), plan)
		}
		if plan.Status == "source-unavailable" {
			if a.json {
				_ = a.writeJSON(cmd, plan)
			}
			return selfupdate.ErrSourceUnavailable
		}
		if !force && plan.UpToDate() {
			result := plan.Result
			result.Status = "up-to-date"
			if a.json {
				return a.writeJSON(cmd, result)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "lazypueue is already current.")
			return nil
		}
		forceDevelopment := force && plan.Installation.BuildKind == "development" && plan.Installation.IdentityValid && plan.Installation.Manager == "" && plan.Installation.Method == "go-install"
		if !plan.CanUpgrade && !forceDevelopment {
			return fmt.Errorf("%s", plan.Reason)
		}
		prompt := fmt.Sprintf("Replace %s with verified lazypueue %s", plan.Installation.ResolvedPath, plan.LatestVersion)
		if len(plan.ManagerCommand) > 0 {
			prompt = "Let Homebrew upgrade the inspected lazypueue formula: " + strings.Join(plan.ManagerCommand, " ")
		}
		if err := a.confirm(cmd, prompt); err != nil {
			return err
		}
		progress := cmd.ErrOrStderr()
		if a.json {
			progress = io.Discard
		}
		result, err := apply(cmd.Context(), plan, selfupdate.ApplyOptions{Force: force}, progress)
		if a.json {
			if e := a.writeJSON(cmd, result); e != nil {
				return e
			}
		} else if result.Message != "" {
			fmt.Fprintln(cmd.OutOrStdout(), result.Message)
		} else if result.Status != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%s)\n", result.Status, result.LatestVersion, result.Installation.ResolvedPath)
		}
		return err
	}
	return scoopupgrade.Wrap(cmd, scoopupgrade.Product{Binary: "lazypueue", Module: "github.com/daviddwlee84/lazypueue", Main: "github.com/daviddwlee84/lazypueue"}, scoopupgrade.CommandOptions{Before: func(*cobra.Command) error { return a.commandOnly() }})
}
func printSelfPlan(w io.Writer, p selfupdate.Plan) error {
	for _, line := range p.Review {
		if _, err := fmt.Fprintln(w, oneLine(line)); err != nil {
			return err
		}
	}
	if len(p.ManagerCommand) > 0 {
		_, err := fmt.Fprintf(w, "Homebrew selects its available version during apply · Can apply: %t\n", p.CanUpgrade)
		return err
	}
	_, err := fmt.Fprintf(w, "Update available: %t · Can apply: %t\n", p.UpdateAvailable, p.CanUpgrade)
	return err
}

func (a *app) backendCommand() *cobra.Command {
	parent := &cobra.Command{Use: "backend", Short: "Inspect and safely maintain the Pueue installation on one host", Args: noArgs, RunE: a.groupHelp}
	parent.AddCommand(a.backendMaintenanceCommand(false), a.backendMaintenanceCommand(true))
	return parent
}
func (a *app) backendMaintenanceCommand(apply bool) *cobra.Command {
	name, short := "status", "Inspect Pueue ownership, available version, daemon service, and upgrade safety"
	if apply {
		name = "upgrade"
		short = "Upgrade an owned Pueue package and its verified idle daemon"
	}
	var check bool
	cmd := &cobra.Command{Use: name, Short: short, Args: noArgs}
	if apply {
		cmd.Flags().BoolVar(&check, "check", false, "Show maintenance plan without changing queues, packages, or services")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		service := a.options.Maintenance
		if service == nil {
			service = maintenance.New(a.options.Backend)
		}
		checking := !apply || check || a.dryRun
		all := a.connection == "all" || (a.connection == "" && cfg.DefaultConnection == "all")
		if !checking && all && len(cfg.Connections) > 1 {
			return usage("backend upgrade requires --connection ID; 'all' is check-only")
		}
		if !checking && a.connection == "all" {
			return usage("backend upgrade requires --connection ID; 'all' is check-only")
		}
		connections := cfg.Connections
		if !all || !checking {
			c, err := a.target(cfg, true)
			if err != nil {
				return err
			}
			connections = []core.Connection{c}
		}
		plans := make([]maintenance.Plan, 0, len(connections))
		var failures []string
		for _, c := range connections {
			plan, e := service.Check(cmd.Context(), c)
			if e != nil {
				plan.Connection = c
				plan.Reason = e.Error()
				failures = append(failures, c.ID)
			}
			plans = append(plans, plan)
		}
		if checking {
			if a.json {
				if !all && len(plans) == 1 {
					err = a.writeJSON(cmd, plans[0])
				} else {
					err = a.writeJSON(cmd, map[string]any{"connections": plans})
				}
				if err != nil {
					return err
				}
			} else {
				for _, p := range plans {
					printMaintenancePlan(cmd.OutOrStdout(), p)
				}
			}
			if len(failures) > 0 {
				return fmt.Errorf("maintenance inspection failed for %v", failures)
			}
			return nil
		}
		if len(plans) != 1 {
			return usage("choose one configured connection")
		}
		plan := plans[0]
		if !plan.CanApply {
			if a.json {
				_ = a.writeJSON(cmd, plan)
			}
			return fmt.Errorf("maintenance cannot apply: %s", plan.Reason)
		}
		if !a.json && !a.yes {
			printMaintenancePlan(cmd.ErrOrStderr(), plan)
		}
		if err := a.confirm(cmd, fmt.Sprintf("Upgrade Pueue %s → %s on %s, stop/start %s, and restore its previous queue scheduling", plan.CurrentVersion, plan.CandidateVersion, plan.Connection.DisplayName(), plan.ServiceName)); err != nil {
			return err
		}
		progress := cmd.ErrOrStderr()
		if a.json {
			progress = io.Discard
		}
		result, err := service.Apply(cmd.Context(), plan, progress)
		if a.json {
			if e := a.writeJSON(cmd, result); e != nil {
				return e
			}
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), result.Message)
			if result.ReceiptPath != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Receipt:", result.ReceiptPath)
			}
		}
		return err
	}
	return cmd
}
func printMaintenancePlan(w io.Writer, p maintenance.Plan) {
	fmt.Fprintf(w, "%s · Pueue %s → %s · owner %s\n", oneLine(p.Connection.DisplayName()), p.CurrentVersion, p.CandidateVersion, p.Owner)
	for _, line := range p.Review {
		fmt.Fprintln(w, oneLine(line))
	}
	if len(p.Review) == 0 && p.Reason != "" {
		fmt.Fprintln(w, oneLine(p.Reason))
	}
	fmt.Fprintf(w, "Can apply: %t\n", p.CanApply)
}
