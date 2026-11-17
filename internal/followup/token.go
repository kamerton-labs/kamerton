// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package followup

import (
	"crypto/rand"
	"encoding/hex"
)

// TokenKey is the key of the webhook token in its Secret.
const TokenKey = "token"

// NewToken returns a random bearer token for the webhook (256 bits, hex).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
