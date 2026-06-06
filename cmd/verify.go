package cmd

import (
	syncer "github.com/andrew-avinante/JellySynnc/internal/sync"
	"github.com/spf13/cobra"
)

var verifyApply bool

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Check synced items still exist on the target and report (or remove) stale DB entries",
	Long: "Verify reconciles the synced_items table against reality on the target. " +
		"A synced item is stale when its .strm file is missing on disk or the target " +
		"Jellyfin library no longer indexes it by provider ID. By default stale entries " +
		"are only reported (dry-run); pass --apply to delete them from the database.\n\n" +
		"Note: items matched on the target only by name or episode number at sync time " +
		"may show as \"not on target\" here, since verify can only match by provider ID. " +
		"Review the dry-run output before using --apply.",
	RunE: runVerify,
}

func init() {
	verifyCmd.Flags().BoolVar(&verifyApply, "apply", false, "delete stale entries from the database (default is dry-run)")
	rootCmd.AddCommand(verifyCmd)
}

func runVerify(cmd *cobra.Command, args []string) error {
	cfg, database, err := setup(cfgFile)
	if err != nil {
		return err
	}
	defer database.Close()

	return syncer.New(cfg, database).Verify(cmd.Context(), verifyApply)
}
