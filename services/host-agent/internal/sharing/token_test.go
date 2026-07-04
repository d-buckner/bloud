package sharing

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret-key-for-hmac-signing-at-least-32-chars"

func TestGenerateToken_ProducesSignedToken(t *testing.T) {
	payload := InvitePayload{
		AppID:         "navidrome",
		AppName:       "Navidrome",
		HostLabel:     "Alice's Server",
		TailnetAddr:   "100.64.1.2",
		NodeShareLink: "https://login.tailscale.com/admin/invite/abc123",
	}

	token, err := GenerateToken(payload, testSecret)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	// Token must have payload.signature format
	parts := strings.SplitN(token, ".", 2)
	require.Len(t, parts, 2, "token should have payload.signature format")
	assert.NotEmpty(t, parts[0], "payload part should not be empty")
	assert.NotEmpty(t, parts[1], "signature part should not be empty")
}

func TestDecodeToken_RoundTrips(t *testing.T) {
	original := InvitePayload{
		AppID:         "jellyfin",
		AppName:       "Jellyfin",
		HostLabel:     "Bob's NAS",
		TailnetAddr:   "ts-jellyfin.tail1275sa.ts.net",
		NodeShareLink: "https://login.tailscale.com/admin/invite/xyz789",
	}

	token, err := GenerateToken(original, testSecret)
	require.NoError(t, err)

	decoded, err := DecodeToken(token, testSecret)
	require.NoError(t, err)

	assert.Equal(t, original.AppID, decoded.AppID)
	assert.Equal(t, original.AppName, decoded.AppName)
	assert.Equal(t, original.HostLabel, decoded.HostLabel)
	assert.Equal(t, original.TailnetAddr, decoded.TailnetAddr)
	assert.Equal(t, original.NodeShareLink, decoded.NodeShareLink)
	assert.True(t, decoded.ExpiresAt > time.Now().Unix(), "token should not be expired yet")
}

func TestDecodeToken_RejectsExpiredToken(t *testing.T) {
	payload := InvitePayload{
		AppID:         "navidrome",
		AppName:       "Navidrome",
		HostLabel:     "Alice's Server",
		TailnetAddr:   "100.64.1.2",
		NodeShareLink: "https://example.com",
		ExpiresAt:     time.Now().Add(-1 * time.Hour).Unix(),
	}

	token := signPayload(t, payload, testSecret)

	_, err := DecodeToken(token, testSecret)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token expired")
}

func TestDecodeToken_RejectsTamperedSignature(t *testing.T) {
	payload := InvitePayload{
		AppID:         "navidrome",
		AppName:       "Navidrome",
		HostLabel:     "Alice's Server",
		TailnetAddr:   "100.64.1.2",
		NodeShareLink: "https://example.com",
	}

	token, err := GenerateToken(payload, testSecret)
	require.NoError(t, err)

	// Flip last character of signature
	tampered := token[:len(token)-1] + "X"
	_, err = DecodeToken(tampered, testSecret)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token signature")
}

func TestDecodeToken_RejectsTamperedPayload(t *testing.T) {
	payload := InvitePayload{
		AppID:         "navidrome",
		AppName:       "Navidrome",
		HostLabel:     "Alice's Server",
		TailnetAddr:   "100.64.1.2",
		NodeShareLink: "https://example.com",
	}

	token, err := GenerateToken(payload, testSecret)
	require.NoError(t, err)

	parts := strings.SplitN(token, ".", 2)
	require.Len(t, parts, 2)

	// Modify payload but keep original signature
	tampered := "dGFtcGVyZWQ" + "." + parts[1]
	_, err = DecodeToken(tampered, testSecret)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token signature")
}

func TestDecodeToken_RejectsWrongSecret(t *testing.T) {
	payload := InvitePayload{
		AppID:         "navidrome",
		AppName:       "Navidrome",
		HostLabel:     "Alice's Server",
		TailnetAddr:   "100.64.1.2",
		NodeShareLink: "https://example.com",
	}

	token, err := GenerateToken(payload, testSecret)
	require.NoError(t, err)

	_, err = DecodeToken(token, "wrong-secret-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token signature")
}

func TestDecodeToken_RejectsInvalidFormat(t *testing.T) {
	_, err := DecodeToken("no-dot-separator", testSecret)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token format")
}

func TestGenerateToken_RequiresSecret(t *testing.T) {
	_, err := GenerateToken(InvitePayload{AppID: "test"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing secret is required")
}

func TestDecodeToken_RequiresSecret(t *testing.T) {
	_, err := DecodeToken("some.token", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing secret is required")
}

// signPayload is a test helper that manually signs a payload to create tokens
// with controlled expiry values (e.g. already-expired tokens).
func signPayload(t *testing.T, payload InvitePayload, secret string) string {
	t.Helper()
	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signature := computeHMAC(encodedPayload, secret)
	return encodedPayload + "." + signature
}
