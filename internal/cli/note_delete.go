package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/present"
	"github.com/mithrel/ginkgo/internal/util"
	"github.com/mithrel/ginkgo/pkg/api"
	"github.com/spf13/cobra"
)

func newNoteDeleteCmd() *cobra.Command {
	var yes bool
	var dry bool
	var outputMode string
	var filters FilterOpts
	cmd := &cobra.Command{
		Use:   "delete [id...]",
		Short: "Delete notes",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := resolveNamespace(cmd)
			if err := ensureNamespaceConfigured(cmd, ns); err != nil {
				return err
			}
			hasFilters := filters.TagsAny != "" || filters.TagsAll != "" || filters.Since != "" || filters.Until != ""
			if len(args) > 0 && hasFilters {
				return fmt.Errorf("ids cannot be combined with filters")
			}
			if outputMode != "" && !dry {
				return fmt.Errorf("--output can only be used with --dry")
			}

			if len(args) == 0 && !hasFilters {
				if dry {
					return dryRunSelection(cmd, ns, filters, outputMode)
				}
				return deleteNamespaceNotes(cmd, ns, yes)
			}

			if len(args) > 0 {
				if dry {
					entries, err := selectEntriesByIDs(cmd, ns, args)
					if err != nil {
						return err
					}
					return dryRunSelectionWithEntries(cmd, ns, FilterOpts{}, outputMode, entries)
				}
				if len(args) > 1 {
					if err := confirmDelete(fmt.Sprintf("Delete %d notes?", len(args)), "This will permanently delete the selected notes.", yes); err != nil {
						return err
					}
				}
				return deleteByIDs(cmd, ns, args)
			}

			entries, err := selectEntries(cmd, ns, filters)
			if err != nil {
				return err
			}
			if dry {
				return dryRunSelectionWithEntries(cmd, ns, filters, outputMode, entries)
			}
			if err := confirmDelete(fmt.Sprintf("Delete %d notes?", len(entries)), "This will permanently delete the matched notes.", yes); err != nil {
				return err
			}
			ids := make([]string, 0, len(entries))
			for _, e := range entries {
				ids = append(ids, e.ID)
			}
			return deleteByIDs(cmd, ns, ids)
		},
	}
	addFilterFlags(cmd, &filters)
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation prompt for bulk deletes")
	cmd.Flags().BoolVar(&dry, "dry", false, "show what would be deleted without making changes")
	cmd.Flags().StringVar(&outputMode, "output", "", "dry-run output mode: plain|pretty|json|ndjson")
	_ = cmd.RegisterFlagCompletionFunc("output", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"plain", "pretty", "json", "ndjson"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func selectEntries(cmd *cobra.Command, ns string, filters FilterOpts) ([]api.Entry, error) {
	sock, err := ipc.SocketPath()
	if err != nil {
		return nil, err
	}
	sinceStr, untilStr, err := util.NormalizeTimeRange(filters.Since, filters.Until)
	if err != nil {
		return nil, err
	}
	any := splitCSV(filters.TagsAny)
	all := splitCSV(filters.TagsAll)
	return fetchAllEntries(cmd.Context(), sock, 0, func(cursor string) ipc.Message {
		return ipc.Message{
			Name:        "note.list",
			Namespace:   ns,
			TagsAny:     any,
			TagsAll:     all,
			Since:       sinceStr,
			Until:       untilStr,
			IncludeBody: false,
		}
	})
}

func selectEntriesByIDs(cmd *cobra.Command, ns string, ids []string) ([]api.Entry, error) {
	sock, err := ipc.SocketPath()
	if err != nil {
		return nil, err
	}
	entries := make([]api.Entry, 0, len(ids))
	for _, id := range ids {
		resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "note.show", ID: id, Namespace: ns})
		if err != nil {
			return nil, err
		}
		if !resp.OK || resp.Entry == nil {
			if resp.Msg != "" {
				return nil, errors.New(resp.Msg)
			}
			return nil, errors.New("not found")
		}
		e := *resp.Entry
		e.Body = ""
		entries = append(entries, e)
	}
	return entries, nil
}

func deleteByIDs(cmd *cobra.Command, ns string, ids []string) error {
	sock, err := ipc.SocketPath()
	if err != nil {
		return err
	}
	for _, id := range ids {
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
	}
	if len(ids) == 1 {
		fmt.Printf("Note ID %s deleted successfully.\n", ids[0])
	} else if len(ids) > 1 {
		fmt.Printf("Deleted %d notes.\n", len(ids))
	}
	return nil
}

func confirmDelete(title, desc string, yes bool) error {
	if yes {
		return nil
	}
	if !term.IsTerminal(os.Stdin.Fd()) {
		return fmt.Errorf("confirmation required; rerun with --yes")
	}
	confirm := false
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(title).
				Description(desc).
				Value(&confirm),
		),
	)
	if err := form.Run(); err != nil {
		return err
	}
	if !confirm {
		return fmt.Errorf("aborted")
	}
	return nil
}

func dryRunSelection(cmd *cobra.Command, ns string, filters FilterOpts, outputMode string) error {
	entries, err := selectEntries(cmd, ns, filters)
	if err != nil {
		return err
	}
	return dryRunSelectionWithEntries(cmd, ns, filters, outputMode, entries)
}

func dryRunSelectionWithEntries(cmd *cobra.Command, ns string, filters FilterOpts, outputMode string, entries []api.Entry) error {
	if outputMode != "" {
		mode, ok := present.ParseMode(strings.ToLower(outputMode))
		if !ok || mode == present.ModeTUI {
			return fmt.Errorf("invalid --output: %s", outputMode)
		}
		opts := present.Options{Mode: mode, JSONIndent: false, Headers: true}
		_ = printSelectionSummary(cmd.ErrOrStderr(), ns, filters, len(entries))
		return renderEntries(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), entries, opts)
	}
	return printSelectionSummary(cmd.OutOrStdout(), ns, filters, len(entries))
}

func printSelectionSummary(w io.Writer, ns string, filters FilterOpts, count int) error {
	_, _ = fmt.Fprintln(w, "Selection:")
	_, _ = fmt.Fprintf(w, "  Namespace: %s\n", ns)
	_, _ = fmt.Fprintln(w, "  Filters:")
	printFilter := func(label, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		_, _ = fmt.Fprintf(w, "    %s: %s\n", label, value)
	}
	printFilter("tags-any", filters.TagsAny)
	printFilter("tags-all", filters.TagsAll)
	printFilter("since", filters.Since)
	printFilter("until", filters.Until)
	_, _ = fmt.Fprintf(w, "\nMatched notes: %d\n", count)
	return nil
}

func deleteNamespaceNotes(cmd *cobra.Command, ns string, yes bool) error {
	if err := confirmDelete("Delete namespace "+ns+"?", "This will permanently delete all local notes in this namespace.", yes); err != nil {
		return err
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
