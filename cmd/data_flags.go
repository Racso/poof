package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// registerDataFlags adds --data-delete and --data-keep to a command.
func registerDataFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("data-delete", false, "delete managed host data")
	cmd.Flags().Bool("data-keep", false, "keep managed host data")
}

// resolveDataIntent resolves what to do with managed host data.
// Returns (purge, abort). If abort is true the caller must exit.
// displayPath is shown in prompts and error messages.
func resolveDataIntent(cmd *cobra.Command, displayPath string) (purge bool, abort bool) {
	dataDelete, _ := cmd.Flags().GetBool("data-delete")
	dataKeep, _ := cmd.Flags().GetBool("data-keep")

	if dataDelete && dataKeep {
		fmt.Fprintln(os.Stderr, "Error: --data-delete and --data-keep are mutually exclusive.")
		return false, true
	}
	if dataDelete {
		return true, false
	}
	if dataKeep {
		return false, false
	}

	// Neither flag given — fall back to interactive prompt or hard failure.
	if isatty.IsTerminal(os.Stdin.Fd()) {
		fmt.Printf("Managed data exists at %s.\nDelete it? [y/N] ", displayPath)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y"), false
	}

	fmt.Fprintf(os.Stderr, "Error: managed data exists at %s.\n", displayPath)
	fmt.Fprintf(os.Stderr, "Re-run with --data-delete or --data-keep to proceed.\n")
	return false, true
}
