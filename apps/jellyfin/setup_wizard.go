// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// waitForSystemInfo polls /System/Info until it answers or the context ends.
// The container health check (curl -sf /System/Info/Public) passes on the
// first 200, but Jellyfin oscillates during first-run init — it briefly
// returns 200 then drops back to 503 "Server is loading" before
// stabilising. A single 503 here would fail PostStart, which the reconciler
// treats as a terminal node ERROR it never retries, so wait out the
// transient instead. ctx is detached from the pass (see PostStart), so the
// 2 s sleep between attempts cannot be cancelled mid-pass; the loop is
// bounded by the deadline on ctx.
func (c *Configurator) waitForSystemInfo(ctx context.Context) (*SystemInfo, error) {
	var info *SystemInfo
	var err error
	for i := range 10 {
		c.logger.Info("DBG retry-loop: iteration", "i", i, "ctx_err", ctx.Err())
		if i > 0 {
			select {
			case <-time.After(2 * time.Second):
				c.logger.Info("DBG retry-loop: sleep completed (2s elapsed)")
			case <-ctx.Done():
				c.logger.Info("DBG retry-loop: ctx.Done() fired during sleep", "ctx_err", ctx.Err())
			}
		}
		info, err = c.getSystemInfo(ctx)
		c.logger.Info("DBG retry-loop: getSystemInfo returned", "err", err, "info_nil", info == nil)
		if err == nil {
			c.logger.Info("DBG retry-loop: success, breaking")
			break
		}
		// Break if the context was cancelled (e.g. orchestrator shutdown)
		// or the deadline expired. A 503 or network error is not a
		// context error — retry it.
		isCanceled := errors.Is(err, context.Canceled)
		isDeadline := errors.Is(err, context.DeadlineExceeded)
		c.logger.Info("DBG retry-loop: error checks", "is_canceled", isCanceled, "is_deadline", isDeadline, "ctx_err", ctx.Err())
		if isCanceled || isDeadline {
			c.logger.Info("DBG retry-loop: context error, breaking")
			break
		}
		c.logger.Info("waiting for Jellyfin API", "attempt", i+1, "error", err)
	}
	c.logger.Info("DBG retry-loop: exited", "final_err", err, "info_nil", info == nil)
	return info, err
}

// awaitWizardCompletion re-polls system info while the wizard still reads as
// pending. Jellyfin 10.11.x may briefly answer 200 mid-initialisation and
// then flip to 503 "Server is loading" while first-run init finishes. Poll
// for a completed read, but a 503 here must never fail PostStart: a failed
// PostStart is a terminal node ERROR the reconciler never retries, and the
// old code did exactly that when a slow cold start outlived the attempt
// cap. Keep the last good read and fall through — completeStartupWizard
// gates on /Startup/Configuration (503 while loading), which absorbs an
// API that hasn't settled. Returns the latest (possibly unchanged) info.

// awaitWizardCompletion re-polls system info while the wizard still reads as
// pending. Jellyfin 10.11.x may briefly answer 200 mid-initialisation and
// then flip to 503 "Server is loading" while first-run init finishes. Poll
// for a completed read, but a 503 here must never fail PostStart: a failed
// PostStart is a terminal node ERROR the reconciler never retries, and the
// old code did exactly that when a slow cold start outlived the attempt
// cap. Keep the last good read and fall through — completeStartupWizard
// gates on /Startup/Configuration (503 while loading), which absorbs an
// API that hasn't settled. Returns the latest (possibly unchanged) info.
func (c *Configurator) awaitWizardCompletion(ctx context.Context, info *SystemInfo) *SystemInfo {
	if info.StartupWizardCompleted {
		return info
	}
	for i := range 5 {
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
		}
		next, perr := c.getSystemInfo(ctx)
		if perr != nil {
			if errors.Is(perr, context.Canceled) || errors.Is(perr, context.DeadlineExceeded) {
				break
			}
			c.logger.Info("waiting for Jellyfin API (wizard check)", "attempt", i+1, "error", perr)
			continue
		}
		info = next
		if info.StartupWizardCompleted {
			break
		}
	}
	return info
}

// SystemInfo represents the /System/Info response

// SystemInfo represents the /System/Info response
type SystemInfo struct {
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ID                     string `json:"Id"`
}

// getSystemInfo fetches the system info from Jellyfin

// getSystemInfo fetches the system info from Jellyfin
func (c *Configurator) getSystemInfo(ctx context.Context) (*SystemInfo, error) {
	url := c.getBaseURL() + "/System/Info/Public"
	c.logger.Info("DBG getSystemInfo: start", "url", url, "ctx_err", ctx.Err())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		c.logger.Error("DBG getSystemInfo: NewRequestWithContext failed", "error", err)
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.logger.Error("DBG getSystemInfo: Do failed", "error", err, "is_ctx_canceled", errors.Is(err, context.Canceled), "is_deadline", errors.Is(err, context.DeadlineExceeded))
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	c.logger.Info("DBG getSystemInfo: got response", "status", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.logger.Warn("DBG getSystemInfo: non-200 status", "status", resp.StatusCode, "body", string(body[:min(len(body), 200)]))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var info SystemInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	return &info, nil
}

// waitForStartupWizardReady waits for Jellyfin's startup wizard API to be ready.
// Even after /health returns OK, Jellyfin may still return 503 with HTML during initialization.
// In Jellyfin 10.11.9+, /Startup/Configuration returns 401 when the wizard is already complete.

// waitForStartupWizardReady waits for Jellyfin's startup wizard API to be ready.
// Even after /health returns OK, Jellyfin may still return 503 with HTML during initialization.
// In Jellyfin 10.11.9+, /Startup/Configuration returns 401 when the wizard is already complete.
func (c *Configurator) waitForStartupWizardReady(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Configuration"

	for i := 0; i < 60; i++ { // Wait up to 60 seconds
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		contentType := resp.Header.Get("Content-Type")
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK && (contentType == "application/json" || contentType == "application/json; charset=utf-8") {
			c.logger.Info("startup wizard API ready")
			return nil
		}

		// Jellyfin 10.11.9+ returns 401 when the wizard is already complete
		// (the endpoint moves behind auth). Treat this as "already done".
		if resp.StatusCode == http.StatusUnauthorized {
			c.logger.Info("startup wizard already complete (API returned 401)")
			return nil
		}

		c.logger.Info("waiting for startup wizard API", "status", resp.StatusCode, "content_type", contentType)
		time.Sleep(time.Second)
	}

	return fmt.Errorf("startup wizard API not ready after 60 seconds")
}

// completeStartupWizard completes the Jellyfin initial setup wizard

// completeStartupWizard completes the Jellyfin initial setup wizard
func (c *Configurator) completeStartupWizard(ctx context.Context) error {
	// Wait for the startup wizard API to be ready
	// Jellyfin returns 503 with HTML while initializing, even if /health returns OK
	if err := c.waitForStartupWizardReady(ctx); err != nil {
		return fmt.Errorf("waiting for startup wizard: %w", err)
	}

	// Step 1: Set initial configuration
	if err := c.setStartupConfiguration(ctx); err != nil {
		return fmt.Errorf("setting startup configuration: %w", err)
	}

	// Step 2: Create the bootstrap admin user
	adminPassword, err := c.resolveAdminPassword()
	if err != nil {
		return err
	}
	if err := c.setStartupUser(ctx, bootstrapUsername, adminPassword); err != nil {
		return fmt.Errorf("creating startup user: %w", err)
	}

	// Step 3: Configure remote access
	if err := c.setRemoteAccess(ctx); err != nil {
		return fmt.Errorf("setting remote access: %w", err)
	}

	// Step 4: Mark wizard as complete
	if err := c.completeWizard(ctx); err != nil {
		return fmt.Errorf("completing wizard: %w", err)
	}

	return nil
}

// setStartupConfiguration sets the initial configuration

// setStartupConfiguration sets the initial configuration
func (c *Configurator) setStartupConfiguration(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Configuration"

	config := map[string]interface{}{
		"UICulture":                 "en-US",
		"MetadataCountryCode":       "US",
		"PreferredMetadataLanguage": "en",
	}

	body, _ := json.Marshal(config)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// setStartupUser creates the first admin user
// Jellyfin auto-creates an initial user internally, but it may take a moment to be ready.
// We first wait for GET /Startup/User to succeed, then POST to update it.

// setStartupUser creates the first admin user
// Jellyfin auto-creates an initial user internally, but it may take a moment to be ready.
// We first wait for GET /Startup/User to succeed, then POST to update it.
func (c *Configurator) setStartupUser(ctx context.Context, username, password string) error {
	url := c.getBaseURL() + "/Startup/User"

	// Wait for the initial user to be available (Jellyfin creates it asynchronously)
	var lastErr error
	for i := 0; i < 10; i++ {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			// Initial user is ready, proceed with update
			break
		}

		lastErr = fmt.Errorf("GET /Startup/User returned %d", resp.StatusCode)
		time.Sleep(500 * time.Millisecond)
	}

	if lastErr != nil {
		c.logger.Warn("initial user not ready after retries", "error", lastErr)
	}

	// Now update the user with our credentials
	user := map[string]string{
		"Name":     username,
		"Password": password,
	}

	body, _ := json.Marshal(user)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// setRemoteAccess configures remote access settings

// setRemoteAccess configures remote access settings
func (c *Configurator) setRemoteAccess(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/RemoteAccess"

	config := map[string]bool{
		"EnableRemoteAccess":         true,
		"EnableAutomaticPortMapping": false,
	}

	body, _ := json.Marshal(config)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// completeWizard marks the startup wizard as complete

// completeWizard marks the startup wizard as complete
func (c *Configurator) completeWizard(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Complete"

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// VirtualFolder represents a Jellyfin library
