// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package jellyfin

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (c *Configurator) ensureLDAPPlugin(ctx context.Context, dataPath string) (bool, error) {
	pluginParent := filepath.Join(dataPath, "config", "plugins")
	pluginDir := filepath.Join(pluginParent, "LDAP-Auth")
	pluginDLL := filepath.Join(pluginDir, "LDAP-Auth.dll")
	if _, err := os.Stat(pluginDLL); err == nil {
		c.logger.Info("LDAP plugin already installed", "path", pluginDLL)
		return false, nil
	}
	c.logger.Info("downloading LDAP plugin", "url", c.pluginURL)
	if err := os.MkdirAll(pluginParent, 0755); err != nil {
		return false, err
	}

	archivePath, err := c.downloadPluginZip(ctx, pluginParent)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(archivePath) }()

	stagingDir, err := extractPluginZip(archivePath, pluginParent)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	if _, err := os.Stat(filepath.Join(stagingDir, "LDAP-Auth.dll")); err != nil {
		return false, fmt.Errorf("plugin archive did not contain LDAP-Auth.dll")
	}
	if err := os.RemoveAll(pluginDir); err != nil {
		return false, err
	}
	if err := os.Rename(stagingDir, pluginDir); err != nil {
		return false, err
	}
	c.logger.Info("LDAP plugin installed", "path", pluginDir)
	return true, nil
}

// downloadPluginZip fetches the LDAP plugin release into a temp zip inside
// dir and verifies its checksum (when configured). Returns the zip path.

// downloadPluginZip fetches the LDAP plugin release into a temp zip inside
// dir and verifies its checksum (when configured). Returns the zip path.
func (c *Configurator) downloadPluginZip(ctx context.Context, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.pluginURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	archive, err := os.CreateTemp(dir, ".ldap-plugin-*.zip")
	if err != nil {
		return "", err
	}
	archivePath := archive.Name()

	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(archive, hash), resp.Body); err != nil {
		_ = archive.Close()
		return "", err
	}
	if err := archive.Close(); err != nil {
		return "", err
	}
	if c.pluginSHA256 != "" && fmt.Sprintf("%x", hash.Sum(nil)) != c.pluginSHA256 {
		return "", fmt.Errorf("download checksum mismatch")
	}
	return archivePath, nil
}

// extractPluginZip unpacks the plugin zip into a fresh temp dir inside
// parent, rejecting archive entries that would escape it (zip-slip guard).

// extractPluginZip unpacks the plugin zip into a fresh temp dir inside
// parent, rejecting archive entries that would escape it (zip-slip guard).
func extractPluginZip(archivePath, parent string) (string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()

	stagingDir, err := os.MkdirTemp(parent, ".LDAP-Auth-*")
	if err != nil {
		return "", err
	}

	for _, file := range reader.File {
		destination := filepath.Join(stagingDir, file.Name)
		if !strings.HasPrefix(filepath.Clean(destination), filepath.Clean(stagingDir)+string(os.PathSeparator)) {
			return "", fmt.Errorf("plugin archive contains invalid path %q", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return "", err
		}
		source, err := file.Open()
		if err != nil {
			return "", err
		}
		mode := file.Mode()
		if mode == 0 {
			mode = 0644
		}
		target, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			_ = source.Close()
			return "", err
		}
		_, copyErr := io.Copy(target, source)
		closeErr := target.Close()
		_ = source.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return stagingDir, nil
}

// jellyfinNetworkConfig returns the desired XML config for network.xml.
