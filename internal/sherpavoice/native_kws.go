//go:build sherpa_onnx && cgo

package sherpavoice

/*
#include <stdlib.h>
#include "nativebridge/ginkgo_kws.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"unsafe"
)

type nativeKWS struct {
	ptr   *C.ginkgo_kws_v1
	label string
}

func openNativeKWS(c NativeConfig) (result *nativeKWS, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w: %v", ErrUnverifiedNativeProfile, err)
		}
	}()
	label, err := readWakeLabel(c.KeywordFile)
	if err != nil {
		return nil, err
	}
	var files []*os.File
	var names []*C.char
	defer func() {
		for _, p := range names {
			C.free(unsafe.Pointer(p))
		}
		for _, f := range files {
			f.Close()
		}
	}()
	for i, path := range []string{c.KWS.Encoder, c.KWS.Decoder, c.KWS.Joiner, c.KWS.Tokens} {
		f, err := sealedKWS(path, i)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
		names = append(names, C.CString(f.Name()))
	}
	alias := C.CString(label)
	defer C.free(unsafe.Pointer(alias))
	k := &nativeKWS{label: label}
	if k.label == "" {
		// Stock's nonalias selection key, not its English spoken form.
		k.label = "HEY1GIH1NGKOW1"
	}
	if c.SelectedKeyword != "" && c.SelectedKeyword != k.label {
		return nil, errors.New("selected keyword label is not in the compiled wake file")
	}
	var message [256]C.char
	if C.ginkgo_kws_v1_abi() != 1 {
		return nil, errors.New("owned KWS ABI mismatch")
	}
	if C.ginkgo_kws_v1_create(names[0], names[1], names[2], names[3], alias, &k.ptr, &message[0], 256) != 0 {
		return nil, errors.New("owned KWS initialization: " + C.GoString(&message[0]))
	}
	return k, nil
}

// events is shared by production and fixture clock checks: only these raw
// integers, copied from the owned native stream, define the emission grid.
func (k *nativeKWS) events(p []float32, finish bool) ([]C.ginkgo_kws_v1_event, error) {
	if k == nil || k.ptr == nil {
		return nil, errors.New("native KWS closed")
	}
	var events [C.GINKGO_KWS_V1_MAX_EVENTS]C.ginkgo_kws_v1_event
	var count C.uint32_t
	var message [256]C.char
	var status C.int
	if finish {
		status = C.ginkgo_kws_v1_finish(k.ptr, &events[0], C.GINKGO_KWS_V1_MAX_EVENTS, &count, &message[0], 256)
	} else {
		if len(p) == 0 || len(p) > C.GINKGO_KWS_V1_MAX_INPUT {
			k.close()
			return nil, errors.New("invalid KWS input length")
		}
		status = C.ginkgo_kws_v1_accept(k.ptr, (*C.float)(unsafe.Pointer(&p[0])), C.uint32_t(len(p)), &events[0], C.GINKGO_KWS_V1_MAX_EVENTS, &count, &message[0], 256)
	}
	if status != 0 {
		k.close()
		return nil, errors.New(C.GoString(&message[0]))
	}
	if count > C.GINKGO_KWS_V1_MAX_EVENTS {
		k.close()
		return nil, errors.New("invalid KWS event count")
	}
	return events[:int(count)], nil
}

func (k *nativeKWS) accept(p []float32) ([]Wake, error) {
	events, err := k.events(p, false)
	if err != nil {
		return nil, err
	}
	wakes := make([]Wake, 0, len(events))
	for _, e := range events {
		// Not phonetic onset/end. Recording remains gated independently.
		wakes = append(wakes, Wake{Phrase: k.label, SpokenForm: "Hey Ginkgo", Span: Span{Start: int64(e.frames[0]) * 640, End: (int64(e.frames[6]) + 1) * 640}})
	}
	return wakes, nil
}

func (k *nativeKWS) close() {
	if k != nil && k.ptr != nil {
		C.ginkgo_kws_v1_destroy(k.ptr)
		k.ptr = nil
	}
}
