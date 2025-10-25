package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion",
		Short: "Generate shell completion scripts",
	}

	gen := &cobra.Command{
		Use:   "generate [bash|zsh|fish]...",
		Short: "Generate completion for specified shells",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for i, sh := range args {
				if i > 0 {
					// separate multiple outputs with a newline comment
					_, _ = fmt.Fprintln(os.Stdout)
					_, _ = fmt.Fprintf(os.Stdout, "# --- %s completion ---\n", sh)
				}
				switch sh {
				case "bash":
					if err := cmd.Root().GenBashCompletion(os.Stdout); err != nil {
						return err
					}
				case "zsh":
					if err := cmd.Root().GenZshCompletion(os.Stdout); err != nil {
						return err
					}
				case "fish":
					if err := cmd.Root().GenFishCompletion(os.Stdout, true); err != nil {
						return err
					}
				default:
					return fmt.Errorf("unknown shell: %s", sh)
				}
			}
			return nil
		},
	}

	cmd.AddCommand(gen)
	return cmd
}
