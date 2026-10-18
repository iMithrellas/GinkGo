package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/internal/ipc"
)

func newContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Attach temporary image context to the next AI note",
	}
	cmd.PersistentFlags().StringP("namespace", "n", "", "namespace receiving the next image-context note")
	registerNamespaceCompletion(cmd)
	cmd.AddCommand(newContextAttachCmd(), newContextCaptureCmd())
	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Clear pending image context",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sendImageContext(cmd, "context.clear", nil)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show pending image context",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sendImageContext(cmd, "context.status", nil)
		},
	})
	return cmd
}

func newContextAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach IMAGE [IMAGE...]",
		Short: "Attach PNG/JPEG files to the next AI note, even when automatic capture is off",
		Args:  cobra.RangeArgs(1, imagecontext.MaxImages),
		RunE: func(cmd *cobra.Command, args []string) error {
			images, err := imagecontext.ReadFiles(cmd.Context(), args)
			if err != nil {
				return err
			}
			return sendImageContext(cmd, "context.stage", images)
		},
	}
}

func newContextCaptureCmd() *cobra.Command {
	var mode, backend string
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Capture the desktop for the next AI note, even when automatic capture is off",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := getApp(cmd).Cfg
			if !cmd.Flags().Changed("mode") && cfg.IsSet("voice.screenshot.mode") {
				mode = cfg.GetString("voice.screenshot.mode")
			}
			if !cmd.Flags().Changed("backend") && cfg.IsSet("voice.screenshot.backend") {
				backend = cfg.GetString("voice.screenshot.backend")
			}
			target, err := imagecontext.ParseMode(mode)
			if err != nil {
				return err
			}
			images, err := imagecontext.Capture(cmd.Context(), imagecontext.DesktopResolver{}, backend, target)
			if err != nil {
				return err
			}
			return sendImageContext(cmd, "context.stage", images)
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "screen", "capture target: window|screen|all-screens (defaults to config)")
	cmd.Flags().StringVar(&backend, "backend", "auto", "capture backend: auto|hyprland (defaults to config)")
	return cmd
}

func sendImageContext(cmd *cobra.Command, name string, images []imagecontext.Image) error {
	namespace := resolveNamespace(cmd)
	if err := ensureNamespaceConfigured(cmd, namespace); err != nil {
		return err
	}
	socket, err := ipc.SocketPath()
	if err != nil {
		return err
	}
	resp, err := ipc.Request(cmd.Context(), socket, ipc.Message{Name: name, Namespace: namespace, Images: images})
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Msg)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), resp.Msg)
	return err
}
