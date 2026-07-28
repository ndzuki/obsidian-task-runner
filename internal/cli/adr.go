package cli

import (
	"fmt"
	"os"

	"github.com/ndzuki/obsidian-task-runner/internal/adr"
	"github.com/spf13/cobra"
)

var writeADRCmd = &cobra.Command{
	Use:   "write-adr <project_dir> <task_id> <title> <body>",
	Short: "Write an accepted project ADR",
	Args:  cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := adr.Write(args[0], args[1], args[2], args[3])
		if err != nil {
			return err
		}
		if err := adr.BuildIndex(args[0]); err != nil {
			return err
		}
		if err := adr.BuildCoverage(args[0]); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
		return err
	},
}

var validateADRCmd = &cobra.Command{
	Use:   "validate-adr <path>",
	Short: "Validate ADR structure",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := adr.Validate(args[0]); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: ADR OK\n", args[0])
		return err
	},
}

var buildADRIndexCmd = &cobra.Command{
	Use:   "build-adr-index <project_dir>",
	Short: "Rebuild ADR index and coverage",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		if _, err := os.Stat(args[0]); err != nil {
			return err
		}
		if err := adr.BuildIndex(args[0]); err != nil {
			return err
		}
		return adr.BuildCoverage(args[0])
	},
}

func init() {
	rootCmd.AddCommand(writeADRCmd, validateADRCmd, buildADRIndexCmd)
}
