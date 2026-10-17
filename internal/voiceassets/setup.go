package voiceassets

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func verifyBytes(ctx context.Context, r io.Reader, a Asset, dst io.Writer) error {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(contextReader{ctx, r}, a.Size+1))
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("size or SHA-256 mismatch for %s (profile %s)", a.Path, Profile)
	}
	return nil
}

// Verify checks exact standard-profile bytes. Custom paths remain supported:
// they must be readable regular files, and native initialization verifies their
// compatibility. KWS overrides still require the audited artifact hashes.
func Verify(ctx context.Context, location Location) error {
	info, err := os.Stat(location.Filename)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("asset must be a nonempty regular file")
	}
	f, err := os.Open(location.Filename)
	if err != nil {
		return err
	}
	defer f.Close()
	if location.Override && !strings.HasPrefix(location.Key, "voice.sherpa.kws.") {
		return ctx.Err()
	}
	return verifyBytes(ctx, f, location.Asset, io.Discard)
}

// Provision downloads missing defaults and verifies installed assets. Check-only
// performs no writes or network requests. Explicit overrides are never replaced.
// A failed or interrupted download cannot become an installed model.
func Provision(ctx context.Context, locations []Location, checkOnly bool, out io.Writer) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	var archive string
	defer func() {
		if archive != "" {
			_ = os.Remove(archive)
		}
	}()
	for _, location := range locations {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := Verify(ctx, location)
		if err == nil {
			fmt.Fprintf(out, "Verified %s\n", location.Path)
			continue
		}
		if !os.IsNotExist(err) || location.Override || checkOnly {
			return fmt.Errorf("verify %s: %w; check the override or run voice setup for missing defaults", location.Filename, err)
		}
		// Do not replace dangling symlinks or files that appeared after verification.
		if _, err := os.Lstat(location.Filename); !os.IsNotExist(err) {
			return fmt.Errorf("asset already exists: %s; inspect it before retrying setup", location.Filename)
		}
		fmt.Fprintf(out, "Installing %s (%d bytes)\n", location.Path, location.Size)
		if location.Member != "" && archive == "" {
			// Reuse the single verified archive for all four KWS files.
			if err := os.MkdirAll(filepath.Dir(location.Filename), 0700); err != nil {
				return err
			}
			f, err := os.CreateTemp(filepath.Dir(location.Filename), ".kws-*.tar.bz2")
			if err != nil {
				return err
			}
			archive = f.Name()
			err = download(ctx, client, kwsArchive, f)
			closeErr := f.Close()
			if err != nil {
				return fmt.Errorf("download KWS archive: %w", err)
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if err := install(ctx, client, location, archive); err != nil {
			return fmt.Errorf("install %s: %w", location.Path, err)
		}
	}
	return nil
}

func download(ctx context.Context, client *http.Client, asset Asset, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	return verifyBytes(ctx, resp.Body, asset, dst)
}

func install(ctx context.Context, client *http.Client, location Location, archive string) error {
	if err := os.MkdirAll(filepath.Dir(location.Filename), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(location.Filename), ".asset-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	switch {
	case location.Content != "":
		err = verifyBytes(ctx, strings.NewReader(location.Content), location.Asset, f)
	case location.Member != "":
		var source *os.File
		source, err = os.Open(archive)
		if err == nil {
			err = extractMember(ctx, tar.NewReader(bzip2.NewReader(source)), location.Asset, f)
			source.Close()
		}
	default:
		err = download(ctx, client, location.Asset, f)
	}
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Link publishes only complete, verified bytes and never overwrites a path.
	return os.Link(f.Name(), location.Filename)
}

func extractMember(ctx context.Context, tr *tar.Reader, asset Asset, dst io.Writer) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if err != nil {
			return fmt.Errorf("read required archive member %s: %w", asset.Member, err)
		}
		if header.Name != asset.Member {
			continue // No archive-supplied path is ever written to disk.
		}
		if header.Typeflag != tar.TypeReg || header.Size != asset.Size {
			return fmt.Errorf("invalid archive member %s", asset.Member)
		}
		return verifyBytes(ctx, tr, asset, dst)
	}
}
