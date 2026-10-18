// Package blobstore provides namespace-scoped storage for immutable blobs.
package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// MaxBytes allows an encrypted/base64 envelope around an 8 MiB image.
const MaxBytes = 12 << 20

// Store saves and loads blobs by namespace and attachment ID.
type Store interface {
	Put(ctx context.Context, namespace, id string, data []byte) error
	Get(ctx context.Context, namespace, id string) ([]byte, error)
	Delete(ctx context.Context, namespace, id string) error
}

// ErrConflict indicates that an ID already holds different data.
var ErrConflict = errors.New("blob already exists with different content")

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,128}$`)

// FileStore stores blobs as private files under a root directory.
type FileStore struct {
	root string
}

// NewFileStore creates a file store. Directories are created on the first write.
func NewFileStore(root string) *FileStore {
	return &FileStore{root: root}
}

func (s *FileStore) path(namespace, id string) (string, error) {
	if namespace == "" || len(namespace) > 256 || !validID.MatchString(id) {
		return "", errors.New("invalid blob namespace or ID")
	}
	hash := sha256.Sum256([]byte(namespace))
	return filepath.Join(s.root, hex.EncodeToString(hash[:]), id), nil
}

// Get reads a blob, returning an error wrapping os.ErrNotExist if it is missing.
func (s *FileStore) Get(ctx context.Context, namespace, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.path(namespace, id)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat blob: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return nil, errors.New("invalid stored blob")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read blob: %w", err)
	}
	if len(data) > MaxBytes {
		return nil, errors.New("stored blob exceeds limit")
	}
	return data, ctx.Err()
}

// Put atomically stores a blob. Writing the same data again succeeds.
func (s *FileStore) Put(ctx context.Context, namespace, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return errors.New("blob exceeds size limit or is empty")
	}
	path, err := s.path(namespace, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create blob directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return fmt.Errorf("create temporary blob: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write blob: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync blob: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close blob: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("publish blob: %w", err)
		}
		existing, err := s.Get(ctx, namespace, id)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, data) {
			return ErrConflict
		}
	}
	return nil
}

// Delete removes a blob. It succeeds if the blob is already absent.
func (s *FileStore) Delete(ctx context.Context, namespace, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(namespace, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}
