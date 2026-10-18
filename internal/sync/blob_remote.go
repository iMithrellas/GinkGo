package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/mithrel/ginkgo/internal/blobstore"
)

// BlobRemote transfers opaque attachment payloads to and from a remote.
type BlobRemote interface {
	Has(ctx context.Context, namespace, id string) (bool, error)
	Get(ctx context.Context, namespace, id string) ([]byte, error)
	Put(ctx context.Context, namespace, id string, data []byte) error
}

type httpBlobRemote struct {
	client *http.Client
	url    string
	token  string
}

func (r *httpBlobRemote) request(ctx context.Context, method, namespace, id string, data []byte) ([]byte, int, error) {
	endpoint := r.url + "/v1/attachments/" + url.PathEscape(id) + "?namespace=" + url.QueryEscape(namespace)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	client := *r.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("attachment transfer failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, blobstore.MaxBytes+1))
	if err != nil || len(body) > blobstore.MaxBytes {
		return nil, resp.StatusCode, errors.New("invalid attachment response")
	}
	return body, resp.StatusCode, nil
}

func (r *httpBlobRemote) Has(ctx context.Context, namespace, id string) (bool, error) {
	_, code, err := r.request(ctx, http.MethodHead, namespace, id, nil)
	if err != nil {
		return false, err
	}
	if code == http.StatusNotFound {
		return false, nil
	}
	if code != http.StatusOK {
		return false, fmt.Errorf("attachment availability check returned HTTP %d", code)
	}
	return true, nil
}

func (r *httpBlobRemote) Get(ctx context.Context, namespace, id string) ([]byte, error) {
	data, code, err := r.request(ctx, http.MethodGet, namespace, id, nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("attachment download returned HTTP %d", code)
	}
	return data, nil
}

func (r *httpBlobRemote) Put(ctx context.Context, namespace, id string, data []byte) error {
	_, code, err := r.request(ctx, http.MethodPut, namespace, id, data)
	if err != nil {
		return err
	}
	// Concurrent uploads can encrypt the same original with different nonces.
	// Downloads verify the original's hash from the note reference.
	if code != http.StatusNoContent && code != http.StatusConflict {
		return fmt.Errorf("attachment upload returned HTTP %d", code)
	}
	return nil
}
