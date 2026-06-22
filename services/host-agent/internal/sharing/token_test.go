package sharing

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret-key-for-hmac-signing"

func validPayload() InvitePayload {
	return InvitePayload{
		ShareID:            "share-123",
		AppID:              "navidrome",
		AppName:            "Navidrome",
		HostLabel:          "Alice's Server",
		SSOStrategy:        "forward-auth",
		SidecarTailnetAddr: "100.64.1.2",
		Exp:                time.Now().Add(TokenExpiry).Unix(),
	}
}

func TestGenerateAndValidateToken(t *testing.T) {
	payload := validPayload()

	token, err := GenerateToken(payload, testSecret)
	require.NoError(t, err)
	assert.Contains(t, token, ".")

	got, err := ValidateToken(token, testSecret)
	require.NoError(t, err)

	assert.Equal(t, payload.ShareID, got.ShareID)
	assert.Equal(t, payload.AppID, got.AppID)
	assert.Equal(t, payload.AppName, got.AppName)
	assert.Equal(t, payload.HostLabel, got.HostLabel)
	assert.Equal(t, payload.SSOStrategy, got.SSOStrategy)
	assert.Equal(t, payload.SidecarTailnetAddr, got.SidecarTailnetAddr)
	assert.Equal(t, payload.Exp, got.Exp)
}

func TestValidateToken_Expired(t *testing.T) {
	payload := validPayload()
	payload.Exp = time.Now().Add(-1 * time.Hour).Unix()

	token, err := GenerateToken(payload, testSecret)
	require.NoError(t, err)

	_, err = ValidateToken(token, testSecret)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestValidateToken_TamperedPayload(t *testing.T) {
	payload := validPayload()

	token, err := GenerateToken(payload, testSecret)
	require.NoError(t, err)

	parts := strings.SplitN(token, ".", 2)
	require.Len(t, parts, 2)

	// Tamper with the payload by replacing with different base64 content
	tampered := base64.RawURLEncoding.EncodeToString([]byte(`{"shareId":"hacked"}`))
	tamperedToken := tampered + "." + parts[1]

	_, err = ValidateToken(tamperedToken, testSecret)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature")
}

func TestValidateToken_WrongSecret(t *testing.T) {
	payload := validPayload()

	token, err := GenerateToken(payload, testSecret)
	require.NoError(t, err)

	_, err = ValidateToken(token, "wrong-secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature")
}

func TestValidateToken_InvalidFormat(t *testing.T) {
	_, err := ValidateToken("not-a-valid-token", testSecret)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token format")
}

func TestGenerateToken_IncludesBypassPaths(t *testing.T) {
	payload := validPayload()
	payload.BypassPaths = []string{"/api/.*", "/health"}

	token, err := GenerateToken(payload, testSecret)
	require.NoError(t, err)

	got, err := ValidateToken(token, testSecret)
	require.NoError(t, err)

	assert.Equal(t, []string{"/api/.*", "/health"}, got.BypassPaths)
}

func TestGenerateToken_EmptySecret(t *testing.T) {
	payload := validPayload()
	_, err := GenerateToken(payload, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret is empty")
}
