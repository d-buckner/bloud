package sharing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TokenExpiry is the duration an invite token remains valid.
const TokenExpiry = 1 * time.Hour

// InvitePayload is the data encoded inside an invite token.
type InvitePayload struct {
	ShareID            string   `json:"shareId"`
	AppID              string   `json:"appId"`
	AppName            string   `json:"appName"`
	HostLabel          string   `json:"hostLabel"`
	SSOStrategy        string   `json:"ssoStrategy"`
	BypassPaths        []string `json:"bypassPaths,omitempty"`
	SidecarTailnetAddr string   `json:"sidecarTailnetAddr"`
	Exp                int64    `json:"exp"`
}

// GenerateToken creates an HMAC-signed invite token.
// Format: base64url(json_payload) + "." + base64url(hmac_sha256(payload_bytes, secret))
func GenerateToken(payload InvitePayload, secret string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("token secret is empty")
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payloadBytes)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payloadBytes)
	sig := mac.Sum(nil)
	encodedSig := base64.RawURLEncoding.EncodeToString(sig)

	return encodedPayload + "." + encodedSig, nil
}

// ValidateToken verifies the HMAC signature and expiry of an invite token.
func ValidateToken(token, secret string) (*InvitePayload, error) {
	if secret == "" {
		return nil, fmt.Errorf("token secret is empty")
	}

	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid token format")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid token payload: %w", err)
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid token signature: %w", err)
	}

	// Verify HMAC
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payloadBytes)
	expectedSig := mac.Sum(nil)
	if !hmac.Equal(sigBytes, expectedSig) {
		return nil, fmt.Errorf("invalid token signature")
	}

	var payload InvitePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("invalid token payload: %w", err)
	}

	if time.Now().Unix() > payload.Exp {
		return nil, fmt.Errorf("token expired")
	}

	return &payload, nil
}
