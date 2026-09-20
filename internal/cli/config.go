package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

func (a *app) configCommand() *cobra.Command {
	parent := &cobra.Command{Use: "config", Short: "Inspect and edit lazypueue preferences", Args: noArgs, RunE: a.groupHelp}
	parent.AddCommand(&cobra.Command{Use: "show", Short: "Show effective configuration and selected path", Args: noArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := a.commandOnly(); err != nil {
			return err
		}
		cfg, path, err := a.load()
		if err != nil {
			return err
		}
		if a.json {
			return a.writeJSON(cmd, map[string]any{"path": path, "config": cfg})
		}
		data, err := toml.Marshal(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "# %s\n%s", path, data)
		return nil
	}})
	parent.AddCommand(&cobra.Command{Use: "edit", Short: "Open the selected config in VISUAL/EDITOR, then validate it", Args: noArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if a.json || a.interactive {
			return usage("config edit cannot be combined with --json or --interactive")
		}
		path, err := config.ResolvePath(a.configPath)
		if err != nil {
			return usage("%v", err)
		}
		if a.dryRun {
			fmt.Fprintf(cmd.OutOrStdout(), "Would edit %s\n", path)
			return nil
		}
		if !a.options.IsTerminal() {
			return usage("config edit requires an input and output terminal; edit %s directly", path)
		}
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			editor = "vi"
		}
		argv, err := parseEditor(editor)
		if err != nil {
			return usage("editor: %v", err)
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := config.Save(path, config.Default()); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		child := exec.CommandContext(cmd.Context(), argv[0], append(argv[1:], path)...)
		child.Stdin = cmd.InOrStdin()
		child.Stdout = cmd.OutOrStdout()
		child.Stderr = cmd.ErrOrStderr()
		if err := child.Run(); err != nil {
			return fmt.Errorf("editor: %w", err)
		}
		if _, err := config.Load(path); err != nil {
			return usage("saved configuration is invalid (your edits remain in %s): %v", path, err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Configuration valid:", path)
		return nil
	}})
	return parent
}
