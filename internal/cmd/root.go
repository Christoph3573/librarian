// Command librarian is an agent-friendly CLI for the Bayerische
// Staatsbibliothek OPAC+ catalog (https://opacplus.bsb-muenchen.de).
//
// Every command supports --help; agents should call `<cmd> --help` before
// first use to see flags and the JSON output contract.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	flagView string
	flagLang string
	flagJSON bool
)

var rootCmd = &cobra.Command{
	Use:   "librarian",
	Short: "Agent-friendly CLI for the Bayerische Staatsbibliothek OPAC+ catalog",
	Long: `Librarian talks to the BSB OPAC+ discovery backend at
https://opacplus.bsb-muenchen.de and helps AI agents (and humans) find books,
inspect their formats and availability, and borrow/download digital media.

Workflow for agents:
  1. librarian auth --help        # log in first: unlocks entitlements (hasAccess),
                                  # request options and ordering; search works anonymously
                                  # but e-license checks underestimate access
  2. librarian research --help    # search for books
  3. librarian inspect --help     # formats + availability of one record
  4. librarian borrow --help      # request physical media / download digital media

Global flags --view/--lang/--json work on every command. --json emits
machine-readable output for agents.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagView, "view", "49BVB_BSB:VU1", "Primo discovery view (VID)")
	rootCmd.PersistentFlags().StringVar(&flagLang, "lang", "de", "interface language (de|en)")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "emit machine-readable JSON output")

	rootCmd.AddCommand(newAuthCmd())
	rootCmd.AddCommand(newResearchCmd())
	rootCmd.AddCommand(newInspectCmd())
	rootCmd.AddCommand(newBorrowCmd())
}

// Execute runs the CLI.
func Execute() error {
	if len(os.Args) == 1 {
		// No args: point agents at --help instead of failing cryptically.
		fmt.Fprintln(os.Stderr, "No command given. Start with `librarian --help`, then `<command> --help`.")
	}
	return rootCmd.Execute()
}
