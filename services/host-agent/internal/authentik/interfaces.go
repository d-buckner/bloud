package authentik

// ClientInterface defines the interface for Authentik API operations.
// This interface enables mocking for testing.
type ClientInterface interface {
	// DeleteAppSSO deletes both the application and provider for an app.
	// This is the main cleanup function to call during app uninstall.
	DeleteAppSSO(appName, displayName, ssoStrategy string) error

	// AddProviderToEmbeddedOutpost adds a proxy provider to the embedded outpost
	AddProviderToEmbeddedOutpost(providerName string) error

	// IsAvailable checks if Authentik is available and the token is valid
	IsAvailable() bool

	// DeleteApplication deletes an Authentik application by slug
	DeleteApplication(slug string) error

	// DeleteOAuth2Provider deletes an OAuth2 provider by name
	DeleteOAuth2Provider(providerName string) error

	// DeleteProxyProvider deletes a proxy provider by name
	DeleteProxyProvider(providerName string) error
}

// Compile-time assertion
var _ ClientInterface = (*Client)(nil)
