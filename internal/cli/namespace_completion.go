package cli

import (
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/spf13/cobra"
)

func registerNamespaceCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("namespace", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		sock, err := ipc.SocketPath()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		resp, err := ipc.Request(cmd.Context(), sock, ipc.Message{Name: "namespace.list"})
		if err != nil || !resp.OK {
			return nil, cobra.ShellCompDirectiveError
		}
		return resp.Namespaces, cobra.ShellCompDirectiveNoFileComp
	})
}
