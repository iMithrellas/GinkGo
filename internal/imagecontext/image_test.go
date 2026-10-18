package imagecontext

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/blobstore"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 4, 3))))
	return data.Bytes()
}

func TestImageValidationAndOriginalStorage(t *testing.T) {
	ctx := context.Background()
	data := testPNG(t)
	im, err := Decode(data, time.Now(), "screen")
	require.NoError(t, err)
	require.Equal(t, 4, im.Width)
	require.Equal(t, "image/png", im.MediaType)
	for _, bad := range [][]byte{nil, []byte("not an image"), data[:len(data)/2], make([]byte, MaxImageBytes+1)} {
		_, err := Decode(bad, time.Time{}, "")
		require.Error(t, err)
	}
	_, err = Validate(ctx, make([]Image, MaxImages+1))
	require.Error(t, err)
	store := NewStore(blobstore.NewFileStore(t.TempDir()))
	refs, err := store.Save(ctx, "work", []Image{im})
	require.NoError(t, err)
	got, err := store.Load(ctx, "work", refs[0])
	require.NoError(t, err)
	require.Equal(t, data, got.Data)
	_, err = store.Load(ctx, "personal", refs[0])
	require.ErrorIs(t, err, os.ErrNotExist)
	badRef := refs[0]
	badRef.Width++
	require.Error(t, Verify(badRef, data))
	badRef = refs[0]
	badRef.SHA256 = "different"
	require.Error(t, Verify(badRef, data))
	path := filepath.Join(t.TempDir(), "input.png")
	require.NoError(t, os.WriteFile(path, data, 0600))
	read, err := ReadFile(ctx, path)
	require.NoError(t, err)
	require.Equal(t, data, read.Data)
	_, err = ReadFile(ctx, filepath.Dir(path))
	require.Error(t, err)
}

func TestPendingNamespaceExpiryAndSingleConsumption(t *testing.T) {
	p := NewPending()
	defer p.Close()
	now := time.Now()
	p.now = func() time.Time { return now }
	im, err := Decode(testPNG(t), now, "screen")
	require.NoError(t, err)
	require.NoError(t, p.Stage("work", []Image{im}))
	require.Empty(t, p.Take("personal"))
	require.Len(t, p.Take("work"), 1)
	require.Empty(t, p.Take("work"))
	require.NoError(t, p.Stage("work", []Image{im}))
	now = now.Add(2 * time.Minute)
	require.Empty(t, p.Take("work"))
	require.Empty(t, p.batches)
}
