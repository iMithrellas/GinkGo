package imagecontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/pkg/api"
)

// Store saves and loads original image attachments.
type Store interface {
	Save(ctx context.Context, namespace string, images []Image) ([]api.ImageAttachment, error)
	Load(ctx context.Context, namespace string, ref api.ImageAttachment) (Image, error)
	Discard(ctx context.Context, namespace string, refs []api.ImageAttachment) error
}

// ImageStore stores original images in a blob store.
type ImageStore struct {
	blobs blobstore.Store
}

// NewStore creates an image store backed by blobs.
func NewStore(blobs blobstore.Store) *ImageStore {
	return &ImageStore{blobs: blobs}
}

// Save stores originals and returns their references, removing partial saves on failure.
func (s *ImageStore) Save(ctx context.Context, namespace string, images []Image) (result []api.ImageAttachment, resultErr error) {
	validated, err := Validate(ctx, images)
	if err != nil {
		return nil, err
	}
	var refs []api.ImageAttachment
	defer func() {
		if resultErr != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, s.Discard(cleanup, namespace, refs))
		}
	}()
	for _, image := range validated {
		hash := sha256.Sum256(image.Data)
		ref := api.ImageAttachment{
			ID:         api.NewID(),
			SHA256:     hex.EncodeToString(hash[:]),
			MediaType:  image.MediaType,
			Size:       int64(len(image.Data)),
			Width:      image.Width,
			Height:     image.Height,
			CapturedAt: image.CapturedAt,
			Label:      image.Label,
		}
		if err := s.blobs.Put(ctx, namespace, ref.ID, image.Data); err != nil {
			return nil, fmt.Errorf("save image %s: %w", ref.ID, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// Discard removes unreferenced originals. Callers must retain them after an
// ambiguous note write, since the note may already reference them.
func (s *ImageStore) Discard(ctx context.Context, namespace string, refs []api.ImageAttachment) error {
	var result error
	for _, ref := range refs {
		if err := s.blobs.Delete(ctx, namespace, ref.ID); err != nil {
			result = errors.Join(result, fmt.Errorf("discard image %s: %w", ref.ID, err))
		}
	}
	return result
}

// Verify checks image data against its stored reference.
func Verify(ref api.ImageAttachment, data []byte) error {
	hash := sha256.Sum256(data)
	if int64(len(data)) != ref.Size || hex.EncodeToString(hash[:]) != ref.SHA256 {
		return errors.New("image attachment integrity check failed")
	}
	image, err := Decode(data, ref.CapturedAt, ref.Label)
	if err != nil {
		return err
	}
	if image.Width != ref.Width || image.Height != ref.Height || image.MediaType != ref.MediaType {
		return errors.New("image attachment metadata mismatch")
	}
	return nil
}

// Load reads and verifies an original image.
func (s *ImageStore) Load(ctx context.Context, namespace string, ref api.ImageAttachment) (Image, error) {
	data, err := s.blobs.Get(ctx, namespace, ref.ID)
	if err != nil {
		return Image{}, fmt.Errorf("load image %s: %w", ref.ID, err)
	}
	if err := Verify(ref, data); err != nil {
		return Image{}, fmt.Errorf("verify image %s: %w", ref.ID, err)
	}
	return Image{
		Data:       data,
		MediaType:  ref.MediaType,
		Width:      ref.Width,
		Height:     ref.Height,
		CapturedAt: ref.CapturedAt,
		Label:      ref.Label,
	}, nil
}
