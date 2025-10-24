package cli

import (
    "os"

    "github.com/spf13/cobra"
)

func newCompletionCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "completion",
        Short: "Generate shell completion scripts",
    }

    cmd.AddCommand(&cobra.Command{
        Use:   "generate bash",
        Short: "Generate Bash completions",
        RunE: func(cmd *cobra.Command, args []string) error {
            return cmd.Root().GenBashCompletion(os.Stdout)
        },
    })
    cmd.AddCommand(&cobra.Command{
        Use:   "generate zsh",
        Short: "Generate Zsh completions",
        RunE: func(cmd *cobra.Command, args []string) error {
            return cmd.Root().GenZshCompletion(os.Stdout)
        },
    })
    cmd.AddCommand(&cobra.Command{
        Use:   "generate fish",
        Short: "Generate Fish completions",
        RunE: func(cmd *cobra.Command, args []string) error {
            return cmd.Root().GenFishCompletion(os.Stdout, true)
        },
    })

    return cmd
}

