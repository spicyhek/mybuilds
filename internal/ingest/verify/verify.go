// Package verify authenticates ingestion requests with constant-time
// comparisons (plan section 14).
package verify

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
)

// HMACSHA256 checks a "sha256=<hex>" signature of body, the format of
// GitHub's X-Hub-Signature-256 header, which CI events reuse.
func HMACSHA256(secret string, body []byte, header string) error {
	if header == "" {
		return errors.New("missing signature")
	}
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return errors.New("signature is not sha256=<hex>")
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil || len(got) != sha256.Size {
		return errors.New("signature is not sha256=<hex>")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return errors.New("signature does not match")
	}
	return nil
}

// Sign returns the "sha256=<hex>" signature of body, for tests and tools.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// BearerToken checks an "Authorization: Bearer <token>" header.
func BearerToken(token, header string) error {
	got, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || got == "" {
		return errors.New("missing bearer token")
	}
	// Compare digests so the comparison takes the same time whatever the
	// token lengths are.
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		return errors.New("token does not match")
	}
	return nil
}
