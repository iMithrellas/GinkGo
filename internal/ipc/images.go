package ipc

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/pkg/api"
)

func toPbImages(images []imagecontext.Image) []*pb.ContextImage {
	var result []*pb.ContextImage
	for _, image := range images {
		result = append(result, &pb.ContextImage{
			Data:       image.Data,
			MediaType:  image.MediaType,
			Width:      int32(image.Width),
			Height:     int32(image.Height),
			CapturedAt: timestamppb.New(image.CapturedAt),
			Label:      image.Label,
		})
	}
	return result
}

func fromPbImages(images []*pb.ContextImage) []imagecontext.Image {
	var result []imagecontext.Image
	for _, image := range images {
		if image == nil {
			continue
		}
		decoded := imagecontext.Image{
			Data:      append([]byte(nil), image.Data...),
			MediaType: image.MediaType,
			Width:     int(image.Width),
			Height:    int(image.Height),
			Label:     image.Label,
		}
		if image.CapturedAt != nil {
			decoded.CapturedAt = image.CapturedAt.AsTime()
		}
		result = append(result, decoded)
	}
	return result
}

func toPbImageRefs(images []api.ImageAttachment) []*pb.ImageAttachment {
	var result []*pb.ImageAttachment
	for _, ref := range images {
		result = append(result, &pb.ImageAttachment{
			Id:         ref.ID,
			Sha256:     ref.SHA256,
			MediaType:  ref.MediaType,
			Size:       ref.Size,
			Width:      int32(ref.Width),
			Height:     int32(ref.Height),
			CapturedAt: timestamppb.New(ref.CapturedAt),
			Label:      ref.Label,
		})
	}
	return result
}

func fromPbImageRefs(images []*pb.ImageAttachment) []api.ImageAttachment {
	var result []api.ImageAttachment
	for _, image := range images {
		if image == nil {
			continue
		}
		ref := api.ImageAttachment{
			ID:        image.Id,
			SHA256:    image.Sha256,
			MediaType: image.MediaType,
			Size:      image.Size,
			Width:     int(image.Width),
			Height:    int(image.Height),
			Label:     image.Label,
		}
		if image.CapturedAt != nil {
			ref.CapturedAt = image.CapturedAt.AsTime()
		}
		result = append(result, ref)
	}
	return result
}
