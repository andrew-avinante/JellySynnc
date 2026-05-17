package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/andrewavinante/JellySynnc/internal/config"
	"github.com/andrewavinante/JellySynnc/internal/db"
	"github.com/andrewavinante/JellySynnc/internal/migrations"
	syncer "github.com/andrewavinante/JellySynnc/internal/sync"
	"github.com/go-co-op/gocron/v2"
	"github.com/spf13/cobra"
)

var listenCmd = &cobra.Command{
	Use:   "listen",
	Short: "Run sync on startup then poll on interval",
	RunE:  runListen,
}

func init() {
	rootCmd.AddCommand(listenCmd)
}

func runListen(cmd *cobra.Command, args []string) error {
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

	s := syncer.New(cfg, database)

	if err := s.Run(cmd.Context()); err != nil {
		slog.Error("initial sync failed", "err", err)
	}

	pollInterval, err := time.ParseDuration(cfg.PollInterval)
	if err != nil {
		return fmt.Errorf("parsing poll_interval %q: %w", cfg.PollInterval, err)
	}

	scheduler, err := gocron.NewScheduler()
	if err != nil {
		return fmt.Errorf("creating scheduler: %w", err)
	}

	if _, err := scheduler.NewJob(
		gocron.DurationJob(pollInterval),
		gocron.NewTask(func() {
			if err := s.Run(context.Background()); err != nil {
				slog.Error("scheduled sync failed", "err", err)
			}
		}),
	); err != nil {
		return fmt.Errorf("scheduling sync job: %w", err)
	}

	scheduler.Start()
	defer scheduler.Shutdown()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	return nil
}
