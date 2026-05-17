package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	syncer "github.com/andrew-avinante/JellySynnc/internal/sync"
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
	cfg, database, err := setup(cfgFile)
	if err != nil {
		return err
	}
	defer database.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s := syncer.New(cfg, database)

	if err := s.Run(ctx); err != nil {
		slog.Error("initial sync failed", "err", err)
	}

	pollInterval, err := time.ParseDuration(cfg.GetPollInterval())
	if err != nil {
		return fmt.Errorf("parsing poll_interval %q: %w", cfg.GetPollInterval(), err)
	}

	scheduler, err := gocron.NewScheduler()
	if err != nil {
		return fmt.Errorf("creating scheduler: %w", err)
	}

	if _, err := scheduler.NewJob(
		gocron.DurationJob(pollInterval),
		gocron.NewTask(func() {
			if err := s.Run(ctx); err != nil {
				slog.Error("scheduled sync failed", "err", err)
			}
		}),
	); err != nil {
		return fmt.Errorf("scheduling sync job: %w", err)
	}

	scheduler.Start()
	defer scheduler.Shutdown()

	<-ctx.Done()
	return nil
}
