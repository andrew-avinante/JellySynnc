package cmd

import (
	syncer "github.com/andrewavinante/JellySynnc/internal/sync"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Run a one-shot sync",
	RunE:  runSync,
}

func init() {
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	cfg, database, err := setup(cfgFile)
	if err != nil {
		return err
	}
	defer database.Close()

	return syncer.New(cfg, database).Run(cmd.Context())
}
