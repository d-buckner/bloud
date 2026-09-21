// SPDX-License-Identifier: AGPL-3.0-only

package appasset

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// buildZip returns an in-memory zip with the given name→content entries.
func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf = &sliceWriter{}
	w := zip.NewWriter(buf)
	for name, content := range entries {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, _ = f.Write([]byte(content))
	}
	require.NoError(t, w.Close())
	return buf.data
}

type sliceWriter struct{ data []byte }

func (s *sliceWriter) Write(p []byte) (int, error) { s.data = append(s.data, p...); return len(p), nil }

func TestInstall_SentinelSkip_NoNetwork(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "plugin")
	require.NoError(t, os.MkdirAll(dest, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "Sentinel.dll"), []byte("x"), 0644))

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	t.Cleanup(srv.Close)

	in := Installer{}
	changed, err := in.Install(context.Background(), Asset{
		Name: "p", Dest: dest, Source: URL(srv.URL + "/p.zip"), Kind: Zip,
		SHA256: "deadbeef", Sentinel: "Sentinel.dll",
	})
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, int32(0), hits, "sentinel present → no network")
}

func TestInstall_ChecksumMismatch_DestUntouched(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "plugin")
	payload := buildZip(t, map[string]string{"a.txt": "hello"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	in := Installer{Sleeper: func(time.Duration) {}}
	_, err := in.Install(context.Background(), Asset{
		Name: "p", Dest: dest, Source: URL(srv.URL + "/p.zip"), Kind: Zip,
		SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
	_, statErr := os.Stat(dest)
	assert.True(t, os.IsNotExist(statErr), "destination untouched on mismatch")
}

func TestInstall_ZipSlipRejected(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "plugin")
	// A zip entry that escapes the staging dir.
	payload := buildZip(t, map[string]string{"../evil.txt": "pwned"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	in := Installer{Sleeper: func(time.Duration) {}}
	_, err := in.Install(context.Background(), Asset{
		Name: "p", Dest: dest, Source: URL(srv.URL + "/p.zip"), Kind: Zip,
		SHA256: sha(payload),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid path")
}

func TestInstall_ZipHappyPath(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "plugin")
	payload := buildZip(t, map[string]string{"plugin.dll": "dll", "lib/dep.dll": "dep"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	in := Installer{Sleeper: func(time.Duration) {}}
	changed, err := in.Install(context.Background(), Asset{
		Name: "p", Dest: dest, Source: URL(srv.URL + "/p.zip"), Kind: Zip,
		SHA256: sha(payload), Sentinel: "plugin.dll",
	})
	require.NoError(t, err)
	assert.True(t, changed)
	assert.FileExists(t, filepath.Join(dest, "plugin.dll"))
	assert.FileExists(t, filepath.Join(dest, "lib", "dep.dll"))
}

func TestInstall_CacheHitAvoidsRedownload(t *testing.T) {
	cache := t.TempDir()
	dest := filepath.Join(t.TempDir(), "plugin")
	payload := buildZip(t, map[string]string{"a": "b"})
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomicInc(&hits)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	in := Installer{CacheDir: cache, Sleeper: func(time.Duration) {}}
	_, err := in.Install(context.Background(), Asset{Name: "p", Dest: dest, Source: URL(srv.URL + "/p.zip"), Kind: Zip, SHA256: sha(payload)})
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomicLoad(&hits))

	dest2 := filepath.Join(t.TempDir(), "plugin2")
	_, err = in.Install(context.Background(), Asset{Name: "p", Dest: dest2, Source: URL(srv.URL + "/p.zip"), Kind: Zip, SHA256: sha(payload)})
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomicLoad(&hits), "second install served from cache")
}

func TestInstall_FileKind(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out", "file.txt")
	content := []byte("hello world")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	t.Cleanup(srv.Close)
	in := Installer{Sleeper: func(time.Duration) {}}
	changed, err := in.Install(context.Background(), Asset{Name: "f", Dest: dest, Source: URL(srv.URL + "/f"), Kind: File, SHA256: sha(content)})
	require.NoError(t, err)
	assert.True(t, changed)
	got, _ := os.ReadFile(dest)
	assert.Equal(t, content, got)
}

func TestInstall_EmbedSource(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out.txt")
	in := Installer{}
	changed, err := in.Install(context.Background(), Asset{
		Name: "e", Dest: dest, Kind: File,
		Source: Embedded(singleFileFS{name: "payload.txt", content: "embedded content"}, "payload.txt"),
	})
	require.NoError(t, err)
	assert.True(t, changed)
	got, _ := os.ReadFile(dest)
	assert.Equal(t, "embedded content", string(got))
}

func TestInstall_TransientDownloadRetried(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "plugin")
	payload := buildZip(t, map[string]string{"a": "b"})
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomicInc(&hits)
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	in := Installer{Sleeper: func(time.Duration) {}}
	_, err := in.Install(context.Background(), Asset{Name: "p", Dest: dest, Source: URL(srv.URL + "/p.zip"), Kind: Zip, SHA256: sha(payload)})
	require.NoError(t, err, "transient download errors are retried")
	assert.GreaterOrEqual(t, atomicLoad(&hits), int32(3))
}
