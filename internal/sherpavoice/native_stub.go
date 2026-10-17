//go:build !sherpa_onnx || !cgo

package sherpavoice

// OpenNative is unavailable in ordinary, dependency-free builds.
func OpenNative(NativeConfig) (Frontend, Recognizer, error) {
	return nil, nil, ErrUnsupported
}

// ProbeProvider requires the optional native backend.
func ProbeProvider(string) (ProviderStatus, error) { return ProviderStatus{}, ErrUnsupported }
