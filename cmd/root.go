package cmd

import (
	"fmt"
	"os"

	"github.com/andrewavinante/JellySynnc/internal/config"
	"github.com/andrewavinante/JellySynnc/internal/db"
	"github.com/andrewavinante/JellySynnc/internal/migrations"
	"github.com/jmoiron/sqlx"
	"github.com/spf13/cobra"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "JellySynnc",
	Short: "Jellyfin sync via .strm files",
}

func Execute(version string) {
	rootCmd.Version = version
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "path to config YAML file")
}

func setup(cfgFile string) (*config.Config, *sqlx.DB, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}
	database, err := db.Connect(cfg.GetDBPath())
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to db: %w", err)
	}
	if err := db.Migrate(database, migrations.FS); err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("running migrations: %w", err)
	}
	return cfg, database, nil
}
