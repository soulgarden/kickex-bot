package cmd

import (
	"os"

	"github.com/rs/zerolog/log"

	"github.com/spf13/cobra"
)

//nolint: gochecknoglobals
var rootCmd = &cobra.Command{}

func Execute() {
	rootCmd.AddCommand(newSpreadCmd())
	rootCmd.AddCommand(newBuyCmd())
	rootCmd.AddCommand(NewArbitrageCmd())

	if err := rootCmd.Execute(); err != nil {
		log.Err(err).Msg("command execution failed")
		os.Exit(1)
	}
}
