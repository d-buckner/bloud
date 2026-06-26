package sharing

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// InvitePayload is the data encoded inside an invite token.
// Security is provided by Tailscale's node sharing auth + social trust,
// so the token is unsigned base64 JSON.
type InvitePayload struct {
	AppID              string `json:"appId"`
	AppName            string `json:"appName"`
	HostLabel          string `json:"hostLabel"`
	SidecarTailnetAddr string `json:"sidecarTailnetAddr"`
	NodeShareLink      string `json:"nodeShareLink"`
}

// GenerateToken creates an unsigned base64url-encoded invite token.
func GenerateToken(payload InvitePayload) (string, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payloadBytes), nil
}

// DecodeToken decodes a base64url-encoded invite token back into its payload.
func DecodeToken(token string) (*InvitePayload, error) {
	payloadBytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid token encoding: %w", err)
	}

	var payload InvitePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("invalid token payload: %w", err)
	}

	return &payload, nil
}
