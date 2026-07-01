package sharing

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateToken_ProducesValidBase64JSON(t *testing.T) {
	payload := InvitePayload{
		AppID:              "navidrome",
		AppName:            "Navidrome",
		HostLabel:          "Alice's Server",
		TailnetAddr: "100.64.1.2",
		NodeShareLink:      "https://login.tailscale.com/admin/invite/abc123",
	}

	token, err := GenerateToken(payload)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	// Token should be valid base64url
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)

	// Decoded bytes should be valid JSON with expected fields
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(decoded, &m))
	assert.Equal(t, "navidrome", m["appId"])
	assert.Equal(t, "Navidrome", m["appName"])
	assert.Equal(t, "Alice's Server", m["hostLabel"])
	assert.Equal(t, "100.64.1.2", m["tailnetAddr"])
	assert.Equal(t, "https://login.tailscale.com/admin/invite/abc123", m["nodeShareLink"])
}

func TestDecodeToken_RoundTrips(t *testing.T) {
	original := InvitePayload{
		AppID:              "jellyfin",
		AppName:            "Jellyfin",
		HostLabel:          "Bob's NAS",
		TailnetAddr: "ts-jellyfin.tail1275sa.ts.net",
		NodeShareLink:      "https://login.tailscale.com/admin/invite/xyz789",
	}

	token, err := GenerateToken(original)
	require.NoError(t, err)

	decoded, err := DecodeToken(token)
	require.NoError(t, err)

	assert.Equal(t, original.AppID, decoded.AppID)
	assert.Equal(t, original.AppName, decoded.AppName)
	assert.Equal(t, original.HostLabel, decoded.HostLabel)
	assert.Equal(t, original.TailnetAddr, decoded.TailnetAddr)
	assert.Equal(t, original.NodeShareLink, decoded.NodeShareLink)
}

func TestDecodeToken_InvalidBase64(t *testing.T) {
	_, err := DecodeToken("not valid base64!!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token encoding")
}

func TestDecodeToken_InvalidJSON(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	_, err := DecodeToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid token payload")
}
