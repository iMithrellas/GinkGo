//go:build sherpa_onnx && cgo && !linux

package sherpavoice

import (
	"errors"
	"os"
)

func sealedKWS(string, int) (*os.File, error) {
	return nil, errors.New("audited KWS requires Linux sealed artifacts")
}
