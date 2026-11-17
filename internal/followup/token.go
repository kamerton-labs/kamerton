// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package followup

import (
	"crypto/rand"
	"encoding/hex"
)

const TokenKey = "token"

// NewToken returns a random 256-bit bearer token, hex-encoded.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
