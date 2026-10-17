//go:build sherpa_onnx && cgo && linux

package sherpavoice

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// sealedKWS prevents mutation between attestation and stock model loading.
func sealedKWS(path string, index int) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open KWS artifact")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64*1024*1024+1))
	if err != nil || len(b) > 64*1024*1024 {
		return nil, errors.New("invalid KWS artifact size")
	}
	if err := attestKWS(b, index); err != nil {
		return nil, err
	}
	fd, err := unix.MemfdCreate("ginkgo-kws", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, errors.New("create sealed KWS artifact")
	}
	out := os.NewFile(uintptr(fd), fmt.Sprintf("/proc/self/fd/%d", fd))
	if _, err := out.Write(b); err != nil {
		out.Close()
		return nil, errors.New("copy sealed KWS artifact")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL); err != nil {
		out.Close()
		return nil, errors.New("seal KWS artifact")
	}
	return out, nil
}
