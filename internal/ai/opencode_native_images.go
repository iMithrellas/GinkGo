//go:build linux

package ai

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// prepareNativeImages accepts the ordinary original-image limits, but returns
// a complete batch of safe native wire copies. It never modifies caller data,
// writes files, starts providers, or returns a partial batch. Work is serial:
// at most one decoded original and one resized image (32 MP each), a bounded
// encoding buffer, and the <=12 MiB encoded batch. Small inputs remain exact.
func prepareNativeImages(ctx context.Context, originals []imagecontext.Image) ([]imagecontext.Image, bool, error) {
	validated, err := validateNativeImages(ctx, originals)
	if err != nil {
		return nil, false, errors.Join(ErrImagePreparation, err)
	}
	prepared := make([]imagecontext.Image, 0, len(validated))
	transformed := false
	for _, original := range validated {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(original.Data) <= openCodeNativeMaxImageBytes {
			prepared = append(prepared, original)
			continue
		}
		copy, err := reduceNativeImage(ctx, original, openCodeNativeMaxImageBytes)
		if err != nil {
			return nil, false, errors.Join(ErrImagePreparation, err)
		}
		prepared = append(prepared, copy)
		transformed = true
	}
	// Re-derive the actual MIME/dimensions and verify decodability and aggregate
	// bounds. This also checks cancellation after the final encode. Every changed
	// image is smaller than its original, so aggregate size cannot increase.
	verified, err := validateNativeImages(ctx, prepared)
	if err != nil {
		return nil, false, errors.Join(ErrImagePreparation, err)
	}
	return verified, transformed, nil
}

// The same original limits as imagecontext.Validate, with context-aware reads
// inside the codecs as well as between images. Do not trust caller-supplied
// MIME/dimensions, and reject an entire batch before returning any item.
func validateNativeImages(ctx context.Context, inputs []imagecontext.Image) ([]imagecontext.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(inputs) > imagecontext.MaxImages {
		return nil, ErrInputLimit
	}
	total := 0
	for _, input := range inputs {
		if len(input.Data) == 0 || len(input.Data) > imagecontext.MaxImageBytes || len(input.Label) > 128 {
			return nil, ErrInputLimit
		}
		total += len(input.Data)
		if total > imagecontext.MaxTotalBytes {
			return nil, ErrInputLimit
		}
	}
	result := make([]imagecontext.Image, 0, len(inputs))
	for _, input := range inputs {
		cfg, format, err := image.DecodeConfig(nativeImageReader{ctx: ctx, Reader: bytes.NewReader(input.Data)})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil || format != "png" && format != "jpeg" {
			return nil, ErrImagePreparation
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > imagecontext.MaxPixels/cfg.Height {
			return nil, ErrInputLimit
		}
		if _, _, err := image.Decode(nativeImageReader{ctx: ctx, Reader: bytes.NewReader(input.Data)}); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrImagePreparation
		}
		copy := input
		copy.Data = append([]byte(nil), input.Data...)
		copy.MediaType, copy.Width, copy.Height = "image/"+format, cfg.Width, cfg.Height
		copy.CapturedAt = copy.CapturedAt.UTC()
		result = append(result, copy)
	}
	return result, ctx.Err()
}

func reduceNativeImage(ctx context.Context, original imagecontext.Image, limit int) (imagecontext.Image, error) {
	if err := ctx.Err(); err != nil {
		return imagecontext.Image{}, err
	}
	if limit <= 0 || limit > openCodeNativeMaxImageBytes {
		return imagecontext.Image{}, ErrInputLimit
	}
	cfg, format, err := image.DecodeConfig(nativeImageReader{ctx: ctx, Reader: bytes.NewReader(original.Data)})
	if err != nil || format != "png" && format != "jpeg" {
		if ctx.Err() != nil {
			return imagecontext.Image{}, ctx.Err()
		}
		return imagecontext.Image{}, ErrImagePreparation
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > imagecontext.MaxPixels/cfg.Height {
		return imagecontext.Image{}, ErrInputLimit
	}
	src, _, err := image.Decode(nativeImageReader{ctx: ctx, Reader: bytes.NewReader(original.Data)})
	if err != nil {
		if ctx.Err() != nil {
			return imagecontext.Image{}, ctx.Err()
		}
		return imagecontext.Image{}, ErrImagePreparation
	}
	bounds := src.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 || bounds.Dx() > imagecontext.MaxPixels/bounds.Dy() {
		return imagecontext.Image{}, ErrInputLimit
	}
	opaque := true
	for y := bounds.Min.Y; y < bounds.Max.Y && opaque; y++ {
		if err := ctx.Err(); err != nil {
			return imagecontext.Image{}, err
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if x%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return imagecontext.Image{}, err
				}
			}
			_, _, _, a := src.At(x, y).RGBA()
			if a != 0xffff {
				opaque = false
				break
			}
		}
	}
	w, h := bounds.Dx(), bounds.Dy()
	// 64 geometrically decreasing attempts reach 1x1 even for a 32M-by-1
	// accepted image. Try lossless full-resolution PNG first; for opaque images
	// prefer full-resolution high-quality JPEG to shrinking text immediately.
	for attempt := 0; attempt < 64; attempt++ {
		if err := ctx.Err(); err != nil {
			return imagecontext.Image{}, err
		}
		candidate := src
		if w != bounds.Dx() || h != bounds.Dy() {
			candidate, err = resizeNativeImage(ctx, src, w, h)
			if err != nil {
				return imagecontext.Image{}, err
			}
		}
		for _, quality := range []int{0, 95, 90} {
			if quality != 0 && !opaque {
				continue // Never flatten transparency to satisfy a byte limit.
			}
			writer := &nativeImageWriter{ctx: ctx, limit: limit}
			if quality == 0 {
				err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(writer, candidate)
			} else {
				err = jpeg.Encode(writer, candidate, &jpeg.Options{Quality: quality})
			}
			if ctx.Err() != nil {
				return imagecontext.Image{}, ctx.Err()
			}
			if errors.Is(err, ErrInputLimit) {
				continue
			}
			if err != nil {
				return imagecontext.Image{}, ErrImagePreparation
			}
			result, err := validateNativeImages(ctx, []imagecontext.Image{{Data: writer.Bytes(), CapturedAt: original.CapturedAt, Label: original.Label}})
			if err != nil {
				return imagecontext.Image{}, err
			}
			return result[0], ctx.Err()
		}
		if w == 1 && h == 1 {
			break
		}
		// Derive the shorter axis from the original aspect ratio each time,
		// rather than accumulating rounding drift over repeated resizes.
		if bounds.Dx() >= bounds.Dy() {
			w = max(1, w*3/4)
			h = max(1, (bounds.Dy()*w+bounds.Dx()/2)/bounds.Dx())
		} else {
			h = max(1, h*3/4)
			w = max(1, (bounds.Dx()*h+bounds.Dy()/2)/bounds.Dy())
		}
	}
	return imagecontext.Image{}, ErrInputLimit
}

type nativeImageReader struct {
	io.Reader
	ctx context.Context
}

func (r nativeImageReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

type nativeImageWriter struct {
	data  []byte
	ctx   context.Context
	limit int
}

func (w *nativeImageWriter) Bytes() []byte { return w.data }

func (w *nativeImageWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > w.limit-len(w.data) {
		return 0, ErrInputLimit
	}
	if w.data == nil {
		w.data = make([]byte, 0, w.limit)
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

// Bilinear resampling interpolates premultiplied channels, preserving alpha and
// avoiding dark halos. Every attempt samples the original, not a previous copy.
func resizeNativeImage(ctx context.Context, src image.Image, width, height int) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if width <= 0 || height <= 0 || width > imagecontext.MaxPixels/height {
		return nil, ErrInputLimit
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	b := src.Bounds()
	for y := 0; y < height; y++ {
		fy := math.Max(0, (float64(y)+0.5)*float64(b.Dy())/float64(height)-0.5)
		y0 := int(fy)
		y1 := min(y0+1, b.Dy()-1)
		wy := fy - float64(y0)
		for x := 0; x < width; x++ {
			if x%256 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			fx := math.Max(0, (float64(x)+0.5)*float64(b.Dx())/float64(width)-0.5)
			x0 := int(fx)
			x1 := min(x0+1, b.Dx()-1)
			wx := fx - float64(x0)
			r0, g0, bl0, a0 := src.At(b.Min.X+x0, b.Min.Y+y0).RGBA()
			r1, g1, bl1, a1 := src.At(b.Min.X+x1, b.Min.Y+y0).RGBA()
			r2, g2, bl2, a2 := src.At(b.Min.X+x0, b.Min.Y+y1).RGBA()
			r3, g3, bl3, a3 := src.At(b.Min.X+x1, b.Min.Y+y1).RGBA()
			mix := func(a, b, c, d uint32) uint8 {
				v := (float64(a)*(1-wx)+float64(b)*wx)*(1-wy) + (float64(c)*(1-wx)+float64(d)*wx)*wy
				return uint8(math.Round(v / 257))
			}
			dst.SetRGBA(x, y, color.RGBA{R: mix(r0, r1, r2, r3), G: mix(g0, g1, g2, g3), B: mix(bl0, bl1, bl2, bl3), A: mix(a0, a1, a2, a3)})
		}
	}
	return dst, ctx.Err()
}
