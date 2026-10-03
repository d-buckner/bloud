// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
)

// FakeAuthentikClient is a fake Authentik client for testing.
type FakeAuthentikClient struct {
	available            bool
	redirectURIs         map[int][]string
	oauthAppBaseURLs     [][]string
	oauthAppClientSecret string
	oidcConfig           *authentik.OIDCConfig
	exchangeCodeCalled   bool
	exchangeCodeResp     *authentik.TokenResponse
	getUserInfoCalled    bool
	userInfo             *authentik.UserInfo
}

// NewFakeAuthentikClient creates a fake Authentik client.
func NewFakeAuthentikClient() *FakeAuthentikClient {
	return &FakeAuthentikClient{
		available:    true,
		redirectURIs: make(map[int][]string),
		oidcConfig: &authentik.OIDCConfig{
			ClientID:     "fake-client-id",
			ClientSecret: "fake-client-secret",
			ProviderID:   1,
		},
		exchangeCodeResp: &authentik.TokenResponse{
			AccessToken: "fake-access-token",
		},
		userInfo: &authentik.UserInfo{
			PreferredUsername: "testuser",
			Groups:            []string{"authentik Admins"},
		},
	}
}

func (f *FakeAuthentikClient) IsAvailable(ctx context.Context) bool { return f.available }

func (f *FakeAuthentikClient) EnsureBloudOAuthApp(ctx context.Context, baseURLs []string, clientSecret string) (*authentik.OIDCConfig, error) {
	f.oauthAppBaseURLs = append(f.oauthAppBaseURLs, baseURLs)
	f.oauthAppClientSecret = clientSecret
	return f.oidcConfig, nil
}

func (f *FakeAuthentikClient) ExchangeCode(ctx context.Context, code, redirectURI, clientID, clientSecret string) (*authentik.TokenResponse, error) {
	f.exchangeCodeCalled = true
	return f.exchangeCodeResp, nil
}

func (f *FakeAuthentikClient) GetUserInfo(ctx context.Context, accessToken string) (*authentik.UserInfo, error) {
	f.getUserInfoCalled = true
	return f.userInfo, nil
}

// fakeSessionStore is an in-memory session store for testing.
type fakeSessionStore struct {
	sessions map[string]*store.Session
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{sessions: make(map[string]*store.Session)}
}

func (f *fakeSessionStore) Create(userID string, username string, role store.Role) (*store.Session, error) {
	s := &store.Session{
		ID:        "fake-session-" + username,
		UserID:    userID,
		Username:  username,
		Role:      role,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}
	f.sessions[s.ID] = s
	return s, nil
}

func (f *fakeSessionStore) Get(sessionID string) (*store.Session, error) {
	s, ok := f.sessions[sessionID]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (f *fakeSessionStore) Delete(sessionID string) error {
	delete(f.sessions, sessionID)
	return nil
}
