package imagecontext

import (
	"context"
	"errors"
	"slices"
	"time"
)

// Mode selects the area of the desktop to capture.
type Mode string

const (
	// Window captures the focused window's visible rectangle.
	Window Mode = "window"
	// Screen captures the focused window's monitor, falling back to the focused monitor.
	Screen Mode = "screen"
	// AllScreens captures each enabled monitor as a separate image.
	AllScreens Mode = "all-screens"
)

// ParseMode parses a desktop capture mode.
func ParseMode(value string) (Mode, error) {
	switch mode := Mode(value); mode {
	case Window, Screen, AllScreens:
		return mode, nil
	default:
		return "", errors.New("screenshot mode must be window, screen or all-screens")
	}
}

// Capturer captures desktop images in its supported modes.
type Capturer interface {
	Modes() []Mode
	Capture(ctx context.Context, mode Mode) ([]Image, error)
}

// Resolver selects a desktop capture backend.
type Resolver interface {
	Resolve(ctx context.Context, backend string) (Capturer, error)
}

const captureTimeout = 5 * time.Second

// Capture checks backend support and captures images within a five-second deadline.
func Capture(parent context.Context, resolver Resolver, backend string, mode Mode) ([]Image, error) {
	if _, err := ParseMode(string(mode)); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, captureTimeout)
	defer cancel()
	capturer, err := resolver.Resolve(ctx, backend)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(capturer.Modes(), mode) {
		return nil, errors.New("screenshot mode is unsupported by this backend")
	}
	images, err := capturer.Capture(ctx, mode)
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, errors.New("screenshot backend returned no images")
	}
	return Validate(ctx, images)
}
