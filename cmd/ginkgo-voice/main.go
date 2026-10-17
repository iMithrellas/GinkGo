// ginkgo-voice is an optional native helper. It is not linked into ginkgod.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mithrel/ginkgo/internal/sherpavoice"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: ginkgo-voice\nPrivate voiceworker v1 over stdin/stdout; models are configured by init.\nNative build requires sherpa_onnx, cgo, sherpa-onnx 1.13.8 and ONNX Runtime.")
	}
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	in, err := commandInput()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ginkgo-voice: open command pipe failed")
		os.Exit(1)
	}
	if err := sherpavoice.Serve(ctx, in, os.Stdout, sherpavoice.NativeRuntime()); err != nil {
		if errors.Is(err, sherpavoice.ErrUnsupported) {
			fmt.Fprintln(os.Stderr, "ginkgo-voice: native backend not built (requires sherpa_onnx and cgo)")
		} else if errors.Is(err, sherpavoice.ErrUnverifiedNativeProfile) {
			fmt.Fprintln(os.Stderr, "ginkgo-voice: native keyword profile not verified")
		} else {
			fmt.Fprintln(os.Stderr, "ginkgo-voice: voice helper failed")
		}
		os.Exit(1)
	}
}

// Serve closes its input to join the reader after failed initialization. Inherited
// os.Stdin is a blocking descriptor: register a nonblocking duplicate with Go's
// poller so Close actually interrupts a pending Read while the parent awaits ready.
func commandInput() (*os.File, error) {
	fd, err := syscall.Dup(int(os.Stdin.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "voiceworker-stdin"), nil
}
