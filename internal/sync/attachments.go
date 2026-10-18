package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/pkg/api"
)

func (s *Service) eventEntry(ev api.Event) (*api.Entry, error) {
	if ev.Type != api.EventUpsert {
		return nil, nil
	}
	if ev.Entry != nil {
		return ev.Entry, nil
	}
	payload := ev.Payload
	if ev.PayloadType == payloadTypeEncV1 {
		var err error
		payload, err = s.decryptPayload(ev.Namespace, payload)
		if err != nil {
			return nil, err
		}
	} else if ev.PayloadType != payloadTypePlainV1 {
		return nil, nil
	}
	entry, _, err := decodePlainPayload(ev.Type, payload)
	return entry, err
}

func (s *Service) pushAttachments(ctx context.Context, rc remoteConfig, events []api.Event) error {
	remote := s.blobRemote(rc)
	seen := map[string]bool{}
	for _, ev := range events {
		entry, err := s.eventEntry(ev)
		if err != nil {
			return err
		}
		if entry == nil {
			continue
		}
		for _, ref := range entry.Images() {
			key := entry.Namespace + "\x00" + ref.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			if err := s.pushAttachment(ctx, remote, entry.Namespace, ref); err != nil {
				return fmt.Errorf("push image %s: %w", ref.ID, err)
			}
		}
	}
	return nil
}

func (s *Service) pushAttachment(ctx context.Context, remote BlobRemote, namespace string, ref api.ImageAttachment) error {
	has, err := remote.Has(ctx, namespace, ref.ID)
	if err != nil || has {
		return err
	}
	data, err := s.blobs.Get(ctx, namespace, ref.ID)
	if err != nil {
		return errors.New("original image attachment unavailable for sync")
	}
	if err := imagecontext.Verify(ref, data); err != nil {
		return err
	}
	kind := payloadTypePlainV1
	if s.e2eeEnabled(namespace) {
		kind, data, err = s.encryptPayload(namespace, data)
		if err != nil {
			return err
		}
	}
	payload := append([]byte(kind+"\n"), data...)
	return remote.Put(ctx, namespace, ref.ID, payload)
}

func (s *Service) pullAttachments(ctx context.Context, rc remoteConfig, events []api.Event) error {
	remote := s.blobRemote(rc)
	seen := map[string]bool{}
	for _, ev := range events {
		entry, err := s.eventEntry(ev)
		if err != nil {
			return err
		}
		if entry == nil {
			continue
		}
		for _, ref := range entry.Images() {
			key := entry.Namespace + "\x00" + ref.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			if err := s.pullAttachment(ctx, remote, entry.Namespace, ref); err != nil {
				return fmt.Errorf("pull image %s: %w", ref.ID, err)
			}
		}
	}
	return nil
}

func (s *Service) pullAttachment(ctx context.Context, remote BlobRemote, namespace string, ref api.ImageAttachment) error {
	local, err := s.blobs.Get(ctx, namespace, ref.ID)
	if err == nil {
		return imagecontext.Verify(ref, local)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	payload, err := remote.Get(ctx, namespace, ref.ID)
	if err != nil {
		return err
	}
	data, err := s.decodeAttachment(namespace, payload)
	if err != nil {
		return err
	}
	if err := imagecontext.Verify(ref, data); err != nil {
		return err
	}
	return s.blobs.Put(ctx, namespace, ref.ID, data)
}

func (s *Service) decodeAttachment(namespace string, payload []byte) ([]byte, error) {
	kind, data, ok := bytes.Cut(payload, []byte{'\n'})
	if !ok {
		return nil, errors.New("invalid attachment envelope")
	}
	switch string(kind) {
	case payloadTypeEncV1:
		return s.decryptPayload(namespace, data)
	case payloadTypePlainV1:
		if s.e2eeEnabled(namespace) {
			return nil, errors.New("unencrypted attachment in encrypted namespace")
		}
		return data, nil
	default:
		return nil, errors.New("unsupported attachment envelope")
	}
}
