package ai

import "context"

// Support describes whether a provider capability is available.
type Support string

const (
	// Unknown means capability metadata is absent; images are skipped.
	Unknown Support = "unknown"
	// Supported means both the selected model and adapter accept images.
	Supported Support = "supported"
	// Unsupported means the model or adapter explicitly lacks image input.
	Unsupported Support = "unsupported"
)

// Capabilities describes the features supported by a model and its adapter.
type Capabilities struct {
	ImageInput Support `json:"image_input"`
}

// CapabilityProvider reports capabilities without submitting note content.
type CapabilityProvider interface {
	Capabilities(context.Context) (Capabilities, error)
}

// ProviderCapabilities returns Unknown for providers without capability discovery.
func ProviderCapabilities(ctx context.Context, provider Provider) (Capabilities, error) {
	if p, ok := provider.(CapabilityProvider); ok {
		return p.Capabilities(ctx)
	}
	return Capabilities{ImageInput: Unknown}, nil
}

// ImageSupport checks the selected provider without starting an inference request.
func (s *Service) ImageSupport(ctx context.Context, name string) Support {
	if !s.options.Enabled || s.resolve == nil {
		return Unsupported
	}
	provider, err := s.resolve(name)
	if err != nil || provider == nil {
		return Unknown
	}
	caps, err := ProviderCapabilities(ctx, provider)
	if err != nil || caps.ImageInput == "" {
		return Unknown
	}
	return caps.ImageInput
}
