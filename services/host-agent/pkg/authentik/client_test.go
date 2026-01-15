package authentik

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteApplication(t *testing.T) {
	tests := []struct {
		name       string
		slug       string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "successful deletion",
			slug:       "miniflux",
			statusCode: http.StatusNoContent,
			wantErr:    false,
		},
		{
			name:       "already deleted (404)",
			slug:       "nonexistent",
			statusCode: http.StatusNotFound,
			wantErr:    false,
		},
		{
			name:       "server error",
			slug:       "error-app",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("expected DELETE, got %s", r.Method)
				}
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("expected Authorization header")
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-token")
			err := client.DeleteApplication(tt.slug)

			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteApplication() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDeleteOAuth2Provider(t *testing.T) {
	tests := []struct {
		name         string
		providerName string
		providers    []ProviderResponse
		wantErr      bool
	}{
		{
			name:         "provider found and deleted",
			providerName: "Miniflux OAuth2 Provider",
			providers: []ProviderResponse{
				{PK: 1, Name: "Other Provider"},
				{PK: 42, Name: "Miniflux OAuth2 Provider"},
			},
			wantErr: false,
		},
		{
			name:         "provider not found",
			providerName: "Nonexistent Provider",
			providers:    []ProviderResponse{},
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					resp := PaginatedResponse{Results: tt.providers}
					resp.Pagination.Count = len(tt.providers)
					json.NewEncoder(w).Encode(resp)
				case http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-token")
			err := client.DeleteOAuth2Provider(tt.providerName)

			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteOAuth2Provider() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDeleteProxyProvider(t *testing.T) {
	tests := []struct {
		name         string
		providerName string
		providers    []ProviderResponse
		wantErr      bool
	}{
		{
			name:         "provider found and deleted",
			providerName: "AdGuard Home Proxy Provider",
			providers: []ProviderResponse{
				{PK: 5, Name: "AdGuard Home Proxy Provider"},
			},
			wantErr: false,
		},
		{
			name:         "provider not found",
			providerName: "Nonexistent Provider",
			providers:    []ProviderResponse{},
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					resp := PaginatedResponse{Results: tt.providers}
					resp.Pagination.Count = len(tt.providers)
					json.NewEncoder(w).Encode(resp)
				case http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-token")
			err := client.DeleteProxyProvider(tt.providerName)

			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteProxyProvider() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDeleteAppSSO(t *testing.T) {
	tests := []struct {
		name        string
		appName     string
		displayName string
		strategy    string
		wantErr     bool
	}{
		{
			name:        "native-oidc app",
			appName:     "miniflux",
			displayName: "Miniflux",
			strategy:    "native-oidc",
			wantErr:     false,
		},
		{
			name:        "forward-auth app",
			appName:     "adguard-home",
			displayName: "AdGuard Home",
			strategy:    "forward-auth",
			wantErr:     false,
		},
		{
			name:        "no SSO strategy",
			appName:     "postgres",
			displayName: "PostgreSQL",
			strategy:    "",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					resp := PaginatedResponse{Results: []ProviderResponse{}}
					json.NewEncoder(w).Encode(resp)
				case http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-token")
			err := client.DeleteAppSSO(tt.appName, tt.displayName, tt.strategy)

			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteAppSSO() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsAvailable(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{
			name:       "available",
			statusCode: http.StatusOK,
			want:       true,
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			want:       false,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-token")
			got := client.IsAvailable()

			if got != tt.want {
				t.Errorf("IsAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}
