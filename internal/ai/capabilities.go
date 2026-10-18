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
	// Uncacheable marks effective support filled by configuration after a transient
	// discovery failure. Outer owners must not turn that fallback into metadata.
	Uncacheable        bool     `json:"-"`
	ImageInput         Support  `json:"image_input"`
	ResolvedModel      string   `json:"resolved_model,omitempty"`
	ImageReason        string   `json:"image_reason,omitempty"`
	ImageMediaTypes    []string `json:"image_media_types,omitempty"`
	MaxImageBytes      int      `json:"max_image_bytes,omitempty"`
	MaxImages          int      `json:"max_images,omitempty"`
	MaxTotalImageBytes int      `json:"max_total_image_bytes,omitempty"`
	MaxImageWidth      int      `json:"max_image_width,omitempty"`
	MaxImageHeight     int      `json:"max_image_height,omitempty"`
	// MaxRequestBytes bounds JSON-encoded Request plus a conservative 64 KiB
	// transport framing reserve. Adapters with larger/different framing should
	// additionally implement ImageRequestPreflighter for their exact encoding.
	MaxRequestBytes int `json:"max_request_bytes,omitempty"`
}

// EffectiveImageSupport intersects selected-model and transport capabilities.
// Configuration may fill missing model metadata, never contradict a known limit.
func EffectiveImageSupport(model, transport Support, override string) Support {
	if model == Unsupported || transport == Unsupported || override == "unsupported" {
		return Unsupported
	}
	if transport != Supported {
		return Unknown
	}
	if model == Supported || ((model == Unknown || model == "") && override == "supported") {
		return Supported
	}
	return Unknown
}

// CapabilityProvider reports capabilities without submitting note content.
type CapabilityProvider interface {
	Capabilities(context.Context) (Capabilities, error)
}

// ImageRequestPreflighter optionally validates a complete image-bearing request
// using the adapter's exact wire framing, without starting inference or sending
// note content. Any rejection skips the entire image batch, not the text note.
type ImageRequestPreflighter interface {
	PreflightImages(context.Context, Request) error
}

// ProviderCapabilities returns Unknown for providers without capability discovery.
func ProviderCapabilities(ctx context.Context, provider Provider) (Capabilities, error) {
	if p, ok := provider.(CapabilityProvider); ok {
		return p.Capabilities(ctx)
	}
	return Capabilities{ImageInput: Unknown}, nil
}

// Warm starts metadata/session discovery only for a daemon-owned enabled AI
// lifetime. Call before installing a wake listener, not in its event callback.
// Failures are best effort and remain retryable at the actual note request.
func (s *Service) Warm(ctx context.Context, name string) {
	if s.options.Registry == nil || !s.options.Enabled || s.resolve == nil {
		return
	}
	provider, err := s.resolve(name)
	if err == nil && provider != nil {
		_, _ = ProviderCapabilities(ctx, provider)
	}
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
