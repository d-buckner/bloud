// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

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

// TokenTTL is the validity duration for invite tokens.
const TokenTTL = 1 * time.Hour

// InvitePayload is the data encoded inside an invite token.
// Tokens are HMAC-SHA256 signed and include an expiry timestamp.
type InvitePayload struct {
	AppID         string `json:"appId"`
	AppName       string `json:"appName"`
	HostLabel     string `json:"hostLabel"`
	TailnetAddr   string `json:"tailnetAddr"`
	NodeShareLink string `json:"nodeShareLink"`
	ExpiresAt     int64  `json:"exp"`
}

// GenerateToken creates an HMAC-SHA256 signed, base64url-encoded invite token.
// Format: base64url(json_payload).base64url(hmac_sha256(json_payload, secret))
// The token expires after TokenTTL.
func GenerateToken(payload InvitePayload, secret string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("signing secret is required")
	}

	payload.ExpiresAt = time.Now().Add(TokenTTL).Unix()

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	encodedPayload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signature := computeHMAC(encodedPayload, secret)

	return encodedPayload + "." + signature, nil
}

// DecodeToken verifies the HMAC-SHA256 signature and expiry, then decodes the payload.
func DecodeToken(token string, secret string) (*InvitePayload, error) {
	if secret == "" {
		return nil, fmt.Errorf("signing secret is required")
	}

	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid token format: expected payload.signature")
	}

	encodedPayload, providedSig := parts[0], parts[1]

	// Verify signature
	expectedSig := computeHMAC(encodedPayload, secret)
	if !hmac.Equal([]byte(providedSig), []byte(expectedSig)) {
		return nil, fmt.Errorf("invalid token signature")
	}

	// Decode payload
	payloadBytes, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return nil, fmt.Errorf("invalid token encoding: %w", err)
	}

	var payload InvitePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("invalid token payload: %w", err)
	}

	// Check expiry
	if time.Now().Unix() > payload.ExpiresAt {
		return nil, fmt.Errorf("token expired")
	}

	return &payload, nil
}

// computeHMAC returns the base64url-encoded HMAC-SHA256 of the message.
func computeHMAC(message, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
