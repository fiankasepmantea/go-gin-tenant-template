// Package idgen generates IDs for primary keys and tokens.
//
// Primary keys use UUID v7: prefixed with a timestamp so they are sequential,
// index-friendly, and still unique. Absolute ordering across instances is guaranteed
// by the {table}_unique (identity) column in the database, not by the UUID.
package idgen

import (
	"crypto/rand"
	"encoding/base64"

	"github.com/google/uuid"
)

// NewUUID creates a new UUID v7.
func NewUUID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// New creates a new UUID v7 as a string, ready to use as {table}_id.
func New() string {
	return NewUUID().String()
}

// NewOpaqueToken creates a 256-bit random token (base64url) for refresh tokens.
// Deliberately NOT sequential: a refresh token must not be guessable.
func NewOpaqueToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("idgen: failed to read random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
