package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/pkg/api"
)

func newNoteImagesCmd() *cobra.Command {
	var directory string
	cmd := &cobra.Command{
		Use:   "images NOTE_ID",
		Short: "List image originals or copy them to a directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace := resolveNamespace(cmd)
			if err := ensureNamespaceConfigured(cmd, namespace); err != nil {
				return err
			}
			socket, err := ipc.SocketPath()
			if err != nil {
				return err
			}
			resp, err := ipc.Request(cmd.Context(), socket, ipc.Message{Name: "note.show", ID: args[0], Namespace: namespace})
			if err != nil {
				return err
			}
			if !resp.OK || resp.Entry == nil {
				if resp.Msg != "" {
					return errors.New(resp.Msg)
				}
				return errors.New("note not found")
			}
			return printNoteImages(cmd, namespace, directory, resp.Entry.Images())
		},
	}
	cmd.Flags().StringVar(&directory, "directory", "", "copy original images here using attachment IDs as filenames")
	return cmd
}

func printNoteImages(cmd *cobra.Command, namespace, directory string, refs []api.ImageAttachment) error {
	for _, ref := range refs {
		if directory == "" {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%dx%d\t%d bytes\n", ref.ID, ref.MediaType, ref.Width, ref.Height, ref.Size); err != nil {
				return err
			}
			continue
		}
		image, err := getApp(cmd).Images.Load(cmd.Context(), namespace, ref)
		if err != nil {
			return fmt.Errorf("image %s unavailable locally; sync may still be pending: %w", ref.ID, err)
		}
		path, err := writeNoteImage(directory, ref.ID, image)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), path); err != nil {
			return err
		}
	}
	return nil
}

func writeNoteImage(directory, id string, image imagecontext.Image) (string, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", fmt.Errorf("create image export directory: %w", err)
	}
	ext := ".png"
	if image.MediaType == "image/jpeg" {
		ext = ".jpg"
	}
	path := filepath.Join(directory, id+ext)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("create image export: %w", err)
	}
	_, writeErr := f.Write(image.Data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("write image export: %w", err)
	}
	return path, nil
}
