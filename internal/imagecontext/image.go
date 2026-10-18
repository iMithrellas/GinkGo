// Package imagecontext provides image capture, validation, and storage.
package imagecontext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"time"
)

const (
	// MaxImages is the maximum number of images per note.
	MaxImages = 8
	// MaxImageBytes is the maximum size of an encoded image.
	MaxImageBytes = 8 << 20
	// MaxTotalBytes leaves room for other fields in a 16 MiB IPC frame.
	MaxTotalBytes = 12 << 20
	// MaxPixels limits the decoded image size.
	MaxPixels = 32_000_000
)

// Image contains PNG or JPEG data and its capture metadata.
type Image struct {
	Data       []byte    `json:"data"`
	MediaType  string    `json:"media_type"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	CapturedAt time.Time `json:"captured_at"`
	Label      string    `json:"label,omitempty"`
}

// Decode validates an image and reads its media type and dimensions.
func Decode(data []byte, at time.Time, label string) (Image, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return Image{}, errors.New("image exceeds size limit or is empty")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return Image{}, errors.New("image must be a valid PNG or JPEG")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxPixels/cfg.Height {
		return Image{}, errors.New("image exceeds pixel limit")
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return Image{}, errors.New("image is truncated or invalid")
	}
	if len(label) > 128 {
		return Image{}, errors.New("image label exceeds limit")
	}
	if at.IsZero() {
		at = time.Now()
	}
	return Image{
		Data:       append([]byte(nil), data...),
		MediaType:  "image/" + format,
		Width:      cfg.Width,
		Height:     cfg.Height,
		CapturedAt: at.UTC(),
		Label:      label,
	}, nil
}

// Validate checks batch limits and derives metadata from each image's data.
func Validate(ctx context.Context, images []Image) ([]Image, error) {
	if len(images) > MaxImages {
		return nil, errors.New("too many context images")
	}
	total := 0
	for _, im := range images {
		total += len(im.Data)
		if total > MaxTotalBytes {
			return nil, errors.New("context images exceed total byte limit")
		}
	}
	var result []Image
	for _, im := range images {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		validated, err := Decode(im.Data, im.CapturedAt, im.Label)
		if err != nil {
			return nil, err
		}
		result = append(result, validated)
	}
	return result, ctx.Err()
}

// ReadFile reads and validates a PNG or JPEG file.
func ReadFile(ctx context.Context, path string) (Image, error) {
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Image{}, fmt.Errorf("stat image: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > MaxImageBytes {
		return Image{}, errors.New("image must be a regular file of at most 8 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return Image{}, fmt.Errorf("open image: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return Image{}, fmt.Errorf("read image: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	return Decode(data, time.Now(), "attached image")
}

// ReadFiles reads image files within the per-note count and size limits.
func ReadFiles(ctx context.Context, paths []string) ([]Image, error) {
	if len(paths) > MaxImages {
		return nil, errors.New("too many context images")
	}
	var images []Image
	total := 0
	for _, path := range paths {
		image, err := ReadFile(ctx, path)
		if err != nil {
			return nil, err
		}
		total += len(image.Data)
		if total > MaxTotalBytes {
			return nil, errors.New("context images exceed total byte limit")
		}
		images = append(images, image)
	}
	return images, nil
}
