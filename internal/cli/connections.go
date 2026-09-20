package cli

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/spf13/cobra"
)

func (a *app) connectionsCommand() *cobra.Command {
	parent := &cobra.Command{Use: "connections", Aliases: []string{"connection"}, Short: "Configure local, SSH, and native Pueue connections", Args: noArgs, RunE: a.groupHelp}
	parent.AddCommand(&cobra.Command{Use: "list", Short: "List configured connections without contacting them", Args: noArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		if a.json {
			return a.writeJSON(cmd, cfg.Connections)
		}
		for _, c := range cfg.Connections {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", c.ID, c.Kind, c.DisplayName())
		}
		return nil
	}})
	parent.AddCommand(a.connectionWriteCommand(false), a.connectionWriteCommand(true))
	parent.AddCommand(&cobra.Command{Use: "remove ID", Short: "Remove a saved connection (leaves its daemon untouched)", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		cfg, path, err := a.load()
		if err != nil {
			return err
		}
		c, err := cfg.Connection(args[0])
		if err != nil || args[0] == "all" {
			return usage("connection %q does not exist", args[0])
		}
		if a.dryRun {
			return a.connectionPlan(cmd, path, "remove", c)
		}
		if err := a.confirm(cmd, fmt.Sprintf("Remove connection %s from %s", c.DisplayName(), path)); err != nil {
			return err
		}
		connections := make([]core.Connection, 0, len(cfg.Connections))
		for _, other := range cfg.Connections {
			if other.ID != c.ID {
				connections = append(connections, other)
			}
		}
		cfg.Connections = connections
		if cfg.DefaultConnection == c.ID {
			cfg.DefaultConnection = "all"
		}
		if err := config.Save(path, cfg); err != nil {
			return err
		}
		if a.json {
			return a.writeJSON(cmd, map[string]string{"removed": c.ID, "config": path})
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Removed connection", c.ID)
		return nil
	}})
	parent.AddCommand(&cobra.Command{Use: "test ID", Short: "Test a connection and report queue reachability", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		if a.dryRun {
			return usage("connections test performs a read; --dry-run is not supported")
		}
		if args[0] == "all" {
			return usage("test requires one connection ID")
		}
		cfg, _, err := a.load()
		if err != nil {
			return err
		}
		c, err := cfg.Connection(args[0])
		if err != nil {
			return usage("%v", err)
		}
		snap, err := a.options.Backend.Snapshot(cmd.Context(), c)
		if a.json {
			value := map[string]any{"connection": c.ID, "reachable": err == nil}
			if err != nil {
				value["error"] = err.Error()
			} else {
				value["tasks"] = len(snap.Tasks)
				value["groups"] = len(snap.Groups)
			}
			if e := a.writeJSON(cmd, value); e != nil {
				return e
			}
		} else if err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "%s is reachable: %d tasks, %d groups\n", c.DisplayName(), len(snap.Tasks), len(snap.Groups))
		}
		return err
	}})
	return parent
}

func (a *app) connectionWriteCommand(editing bool) *cobra.Command {
	operation := "add"
	if editing {
		operation = "edit"
	}
	var fields core.Connection
	cmd := &cobra.Command{Use: operation + " [ID]", Short: strings.ToUpper(operation[:1]) + operation[1:] + " a connection; bare add opens a wizard"}
	f := cmd.Flags()
	f.StringVar(&fields.Name, "name", "", "Display name")
	f.StringVar(&fields.Kind, "kind", "local", "Connection kind: local, ssh, native")
	f.StringVar(&fields.Binary, "binary", "", "Pueue executable (remote path for SSH)")
	f.StringVar(&fields.ConfigPath, "pueue-config", "", "Existing Pueue configuration path")
	f.StringVar(&fields.Profile, "profile", "", "Pueue profile in the existing config")
	f.StringVar(&fields.SSHHost, "ssh-host", "", "SSH alias, or native connection's SSH submission companion")
	f.StringVar(&fields.SSHBinary, "ssh-binary", "", "Remote Pueue executable for SSH submission")
	f.StringVar(&fields.SSHConfig, "ssh-pueue-config", "", "Pueue config path on native connection's SSH companion")
	f.StringVar(&fields.SSHProfile, "ssh-profile", "", "Pueue profile on native connection's SSH companion")
	f.StringVar(&fields.Host, "host", "", "Native TLS host")
	f.IntVar(&fields.Port, "port", 0, "Native TLS port")
	f.StringVar(&fields.CertPath, "cert", "", "Pinned Pueue daemon certificate path")
	f.StringVar(&fields.SecretPath, "secret-file", "", "Shared-secret file path (never the secret itself)")
	f.StringVar(&fields.SocketPath, "socket", "", "Local or forwarded Unix socket path (native mode also needs --secret-file)")
	_ = cmd.RegisterFlagCompletionFunc("kind", choices("local", "ssh", "native"))
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 1 {
			return usage("%s accepts one connection ID", cmd.CommandPath())
		}
		if editing && len(args) != 1 {
			return usage("provide the connection ID to edit; see lazypueue connections edit --help")
		}
		if fields.Kind != "local" && fields.Kind != "ssh" && fields.Kind != "native" {
			return usage("--kind must be local, ssh, or native")
		}
		if cmd.Flags().Changed("port") && (fields.Port < 1 || fields.Port > 65535) {
			return usage("--port must be between 1 and 65535")
		}
		for _, value := range []string{fields.Name, fields.Binary, fields.ConfigPath, fields.Profile, fields.SSHHost, fields.SSHBinary, fields.SSHConfig, fields.SSHProfile, fields.Host, fields.CertPath, fields.SecretPath, fields.SocketPath} {
			if strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return usage("connection fields cannot contain control characters")
			}
		}
		if fields.SSHHost != "" && (strings.HasPrefix(fields.SSHHost, "-") || strings.ContainsAny(fields.SSHHost, " \t\r\n")) {
			return usage("--ssh-host must be an SSH alias or user@host")
		}
		if fields.Host != "" && (strings.HasPrefix(fields.Host, "-") || strings.ContainsAny(fields.Host, " /\t\r\n")) {
			return usage("--host must be a hostname or IP address")
		}
		if len(args) > 0 {
			if err := config.ValidateConnection(core.Connection{ID: args[0], Kind: "local"}); err != nil {
				return usage("%v", err)
			}
		}
		if a.interactive && a.dryRun {
			return usage("--interactive cannot be combined with --dry-run")
		}
		cfg, path, err := a.load()
		if err != nil {
			return err
		}
		initial := core.Connection{Kind: "local"}
		if len(args) > 0 {
			initial.ID = args[0]
			if initial.ID == "all" {
				return usage("'all' is reserved; choose another connection ID")
			}
		}
		if editing {
			initial, err = cfg.Connection(args[0])
			if err != nil {
				return usage("%v", err)
			}
		}
		for _, field := range []struct {
			name string
			dst  *string
			src  string
		}{{"name", &initial.Name, fields.Name}, {"kind", &initial.Kind, fields.Kind}, {"binary", &initial.Binary, fields.Binary}, {"pueue-config", &initial.ConfigPath, fields.ConfigPath}, {"profile", &initial.Profile, fields.Profile}, {"ssh-host", &initial.SSHHost, fields.SSHHost}, {"ssh-binary", &initial.SSHBinary, fields.SSHBinary}, {"ssh-pueue-config", &initial.SSHConfig, fields.SSHConfig}, {"ssh-profile", &initial.SSHProfile, fields.SSHProfile}, {"host", &initial.Host, fields.Host}, {"cert", &initial.CertPath, fields.CertPath}, {"secret-file", &initial.SecretPath, fields.SecretPath}, {"socket", &initial.SocketPath, fields.SocketPath}} {
			if f.Changed(field.name) {
				*field.dst = field.src
			}
		}
		if f.Changed("port") {
			initial.Port = fields.Port
		}
		bare := (!editing && len(args) == 0 || editing) && !changedBusinessFlags(cmd)
		wizard := a.interactive || (bare && !a.json && !a.dryRun && a.options.IsTerminal())
		if wizard {
			result, err := a.runConnectionWizard(cmd, cfg, path, initial, editing)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved connection %s in %s\n", result.ID, path)
			return nil
		}
		if editing && !changedBusinessFlags(cmd) {
			return usage("provide connection fields to edit, or use --interactive")
		}
		if err := config.ValidateConnection(initial); err != nil {
			return usage("%v; see %s --help", err, cmd.CommandPath())
		}
		next := cfg
		next.Connections = append([]core.Connection(nil), cfg.Connections...)
		if err := replaceConnection(&next, initial.ID, initial, editing); err != nil {
			return usage("%v", err)
		}
		if a.dryRun {
			return a.connectionPlan(cmd, path, operation, initial)
		}
		if err := config.Save(path, next); err != nil {
			return err
		}
		if a.json {
			return a.writeJSON(cmd, map[string]any{"connection": initial, "config": path})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Saved connection %s in %s\n", initial.ID, path)
		return nil
	}
	return cmd
}
func replaceConnection(cfg *config.Config, oldID string, c core.Connection, editing bool) error {
	if err := config.ValidateConnection(c); err != nil {
		return err
	}
	for _, existing := range cfg.Connections {
		if existing.ID == c.ID && (!editing || existing.ID != oldID) {
			return fmt.Errorf("connection %q already exists", c.ID)
		}
	}
	for i, existing := range cfg.Connections {
		if editing && existing.ID == oldID {
			cfg.Connections[i] = c
			if cfg.DefaultConnection == oldID {
				cfg.DefaultConnection = c.ID
			}
			return nil
		}
	}
	if editing {
		return fmt.Errorf("connection %q no longer exists", oldID)
	}
	cfg.Connections = append(cfg.Connections, c)
	return nil
}
func (a *app) connectionPlan(cmd *cobra.Command, path, operation string, c core.Connection) error {
	value := map[string]any{"operation": "connection-" + operation, "config": path, "connection": c}
	if a.json {
		return a.writeJSON(cmd, value)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Would %s connection %s (%s) in %s\n", operation, c.ID, c.Kind, path)
	return nil
}
