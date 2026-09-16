// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// Installer installs Assets with a shared content cache. The zero value is
// usable (no cache, default retry).
type Installer struct {
	// CacheDir is the content-addressed cache directory
	// (<data>/asset-cache). Empty disables caching.
	CacheDir string
	// Retry is the default fetch retry policy. Zero → a download-friendly
	// default (long deadline, bounded attempts).
	Retry appclient.RetryPolicy
	// Sleeper overrides the backoff sleep (tests).
	Sleeper func(time.Duration)
}

// Install makes dest hold the asset's payload, returning changed=true only
// when it actually wrote something. It is idempotent: a present sentinel (or
// SkipIf) short-circuits with (false, nil) and no network.
func (in Installer) Install(ctx context.Context, a Asset) (bool, error) {
	if a.Dest == "" {
		return false, errors.New("appasset: Asset.Dest is required")
	}
	if in.alreadyInstalled(a) {
		return false, nil
	}
	if a.Source.URL != "" && a.SHA256 == "" {
		return false, fmt.Errorf("appasset: %s: URL source requires a SHA256", a.Name)
	}

	// Stage the payload as a temp file inside the destination filesystem so
	// the final rename is atomic within one fs.
	payload, err := in.stagePayload(ctx, a)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(payload.tempPath) }()

	// Verify the digest before touching the destination.
	if a.SHA256 != "" && payload.digest != a.SHA256 {
		in.deleteCache(a.SHA256)
		return false, fmt.Errorf("appasset: %s: checksum mismatch (got %s, want %s)", a.Name, payload.digest, a.SHA256)
	}

	if a.Kind == Zip {
		return in.commitZip(ctx, a, payload.tempPath)
	}
	return in.commitFile(a, payload.tempPath)
}

// alreadyInstalled reports the skip condition.
func (in Installer) alreadyInstalled(a Asset) bool {
	if a.SkipIf != nil {
		return a.SkipIf(a.Dest)
	}
	if a.Sentinel != "" {
		if _, err := os.Stat(filepath.Join(a.Dest, a.Sentinel)); err == nil {
			return true
		}
	}
	return false
}

// stagedPayload is a temp file plus its computed digest.
type stagedPayload struct {
	tempPath string
	digest   string
}

// stagePayload resolves the source into a temp file in the destination's
// filesystem and computes its sha256.
func (in Installer) stagePayload(ctx context.Context, a Asset) (*stagedPayload, error) {
	switch {
	case a.Source.URL != "":
		return in.stageURL(ctx, a)
	case a.Source.Embed != nil:
		return in.stageFS(ctx, a, embedReader(a.Source))
	case a.Source.Local != "":
		return in.stageFS(ctx, a, localReader(a.Source.Local))
	default:
		return nil, fmt.Errorf("appasset: %s: no source set", a.Name)
	}
}

// stageURL fetches (or reuses the cache for) a remote asset.
func (in Installer) stageURL(ctx context.Context, a Asset) (*stagedPayload, error) {
	// Cache hit: verify the cached digest, then copy into a fresh temp.
	if cached, ok := in.cachedPath(a.SHA256); ok {
		d, err := hashFile(cached)
		if err == nil && d == a.SHA256 {
			f, ferr := os.Open(cached)
			if ferr == nil {
				tp, dh, cerr := copyToTemp(f, filepath.Dir(a.Dest), "."+a.Name+"-*", a.maxBytes())
				_ = f.Close()
				if cerr == nil {
					return &stagedPayload{tempPath: tp, digest: dh}, nil
				}
			}
		}
		// Poisoned or unreadable cache entry: drop it and re-download.
		in.deleteCache(a.SHA256)
	}

	tp, dh, err := in.download(ctx, a)
	if err != nil {
		return nil, err
	}
	in.storeCache(a.SHA256, tp)
	return &stagedPayload{tempPath: tp, digest: dh}, nil
}

// download streams the URL into a temp file in the destination fs, hashing
// while streaming and capping at MaxBytes, with the fetch retried.
func (in Installer) download(ctx context.Context, a Asset) (string, string, error) {
	client := in.fetchClient(a)
	dir := filepath.Dir(a.Dest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", err
	}
	var (
		tempPath string
		digest   string
	)
	err := client.GET(a.Source.URL).Stream(ctx, func(body io.Reader) error {
		// Fresh temp file per attempt (a retry must not append to a partial).
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
		tp, dh, cerr := copyToTemp(body, dir, "."+a.Name+"-*", a.maxBytes())
		if cerr != nil {
			return cerr
		}
		tempPath, digest = tp, dh
		return nil
	})
	if err != nil {
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
		return "", "", fmt.Errorf("appasset: %s: download: %w", a.Name, err)
	}
	return tempPath, digest, nil
}

// stageFS copies an fs- or local-sourced payload into a temp file.
func (in Installer) stageFS(ctx context.Context, a Asset, open func() (io.ReadCloser, error)) (*stagedPayload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rc, err := open()
	if err != nil {
		return nil, fmt.Errorf("appasset: %s: open source: %w", a.Name, err)
	}
	defer func() { _ = rc.Close() }()
	tp, dh, err := copyToTemp(rc, filepath.Dir(a.Dest), "."+a.Name+"-*", a.maxBytes())
	if err != nil {
		return nil, err
	}
	return &stagedPayload{tempPath: tp, digest: dh}, nil
}

// commitFile moves the staged temp file into place at Dest.
func (in Installer) commitFile(a Asset, tempPath string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(a.Dest), 0755); err != nil {
		return false, err
	}
	if err := os.RemoveAll(a.Dest); err != nil {
		return false, err
	}
	if err := os.Rename(tempPath, a.Dest); err != nil {
		return false, fmt.Errorf("appasset: %s: commit: %w", a.Name, err)
	}
	return true, nil
}

// commitZip unpacks the staged archive into a staging dir, runs Verify, then
// atomically replaces Dest with the staged tree.
func (in Installer) commitZip(ctx context.Context, a Asset, archivePath string) (bool, error) {
	parent := filepath.Dir(a.Dest)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return false, err
	}
	staging, err := os.MkdirTemp(parent, "."+a.Name+"-staged-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := unpackZip(archivePath, staging, a.Strip); err != nil {
		return false, fmt.Errorf("appasset: %s: unpack: %w", a.Name, err)
	}
	if a.Verify != nil {
		if err := a.Verify(staging); err != nil {
			return false, fmt.Errorf("appasset: %s: verify: %w", a.Name, err)
		}
	}
	if err := os.RemoveAll(a.Dest); err != nil {
		return false, err
	}
	if err := os.Rename(staging, a.Dest); err != nil {
		return false, fmt.Errorf("appasset: %s: commit tree: %w", a.Name, err)
	}
	return true, nil
}

// fetchClient builds the HTTP client for a URL fetch with a generous timeout
// (assets can be large) and the asset/installer retry policy.
func (in Installer) fetchClient(a Asset) *appclient.Client {
	retry := a.Retry
	if retry == (appclient.RetryPolicy{}) {
		retry = in.Retry
	}
	if retry == (appclient.RetryPolicy{}) {
		retry = appclient.RetryPolicy{
			MaxAttempts: 5,
			Deadline:    5 * time.Minute,
			Initial:     time.Second,
			MaxInterval: 15 * time.Second,
			Factor:      2,
			Jitter:      0.2,
		}
	}
	c := appclient.New(appclient.Spec{
		Name:    "appasset:" + a.Name,
		BaseURL: "",
		Retry:   retry,
		Timeout: 10 * time.Minute,
	})
	if in.Sleeper != nil {
		c.WithSleeper(in.Sleeper)
	}
	return c
}

// maxBytes returns the effective download cap.
func (a Asset) maxBytes() int64 {
	if a.MaxBytes > 0 {
		return a.MaxBytes
	}
	return defaultMaxBytes
}

// --- helpers ---

// copyToTemp streams src into a temp file in dir, hashing as it goes, capped
// at maxBytes. Returns the temp path and the hex sha256.
func copyToTemp(src io.Reader, dir, pattern string, maxBytes int64) (string, string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", "", err
	}
	tp := tmp.Name()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(src, maxBytes+1))
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tp)
		return "", "", err
	}
	if n > maxBytes {
		_ = tmp.Close()
		_ = os.Remove(tp)
		return "", "", fmt.Errorf("asset exceeds max size %d bytes", maxBytes)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tp)
		return "", "", err
	}
	return tp, hex.EncodeToString(hash.Sum(nil)), nil
}

// hashFile returns the hex sha256 of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func localReader(path string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return os.Open(path) }
}

func embedReader(s Source) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		f, err := s.Embed.Open(s.Name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("embedded asset %q not found: %w", s.Name, err)
			}
			return nil, err
		}
		return f, nil
	}
}