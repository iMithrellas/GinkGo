package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"
)

func newNoteDeleteCmd() *cobra.Command {
	var deleteNamespace bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a note",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := resolveNamespace(cmd)
			if err := ensureNamespaceConfigured(cmd, ns); err != nil {
				return err
			}
			if deleteNamespace {
				if len(args) != 0 {
					return fmt.Errorf("namespace delete does not accept an id")
				}
				return deleteNamespaceNotes(cmd, ns, yes)
			}
			if len(args) != 1 {
				return fmt.Errorf("id is required")
			}
			id := args[0]
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.delete", ID: id, Namespace: ns})
			if err != nil {
				return err
			}
			if !resp.OK {
				if resp.Msg != "" {
					return errors.New(resp.Msg)
				}
				return errors.New("not found")
			}
			fmt.Printf("Note ID %s deleted successfully.\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteNamespace, "namespace-delete", false, "delete all notes in the namespace")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation prompt for namespace delete")
	return cmd
}

func deleteNamespaceNotes(cmd *cobra.Command, ns string, yes bool) error {
	if !yes {
		if !term.IsTerminal(os.Stdin.Fd()) {
			return fmt.Errorf("confirmation required; rerun with --yes")
		}
		confirm := false
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Delete namespace " + ns + "?").
					Description("This will permanently delete all local notes in this namespace.").
					Value(&confirm),
			),
		)
		if err := form.Run(); err != nil {
			return err
		}
		if !confirm {
			return fmt.Errorf("aborted")
		}
	}

	sock, err := ipc.SocketPath()
	if err != nil {
		return err
	}
	resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "namespace.delete", Namespace: ns})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Msg)
	}

	app := getApp(cmd)
	if err := deleteNamespaceKeys(app, ns); err != nil {
		return err
	}
	if err := removeNamespaceConfig(cmd, app, ns); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), resp.Msg)
	return nil
}
