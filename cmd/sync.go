package cmd

import (
	"fmt"

	"github.com/andrewavinante/JellySynnc/internal/config"
	"github.com/andrewavinante/JellySynnc/internal/db"
	"github.com/andrewavinante/JellySynnc/internal/migrations"
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
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	database, err := db.Connect(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("connecting to db: %w", err)
	}
	defer database.Close()

	if err := db.Migrate(database, migrations.FS); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	return syncer.New(cfg, database).Run(cmd.Context())
}
