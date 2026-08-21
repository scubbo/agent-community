package discussion

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

const capabilityPrefix = "acp_"

type capabilityRecord struct {
	KeyID       string       `json:"key_id"`
	TokenDigest string       `json:"token_digest"`
	Permissions []Permission `json:"permissions"`
	CreatedAt   time.Time    `json:"created_at"`
}

func newCapability(now time.Time, permissions []Permission) (string, capabilityRecord, error) {
	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return "", capabilityRecord{}, fmt.Errorf("generate capability key id: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", capabilityRecord{}, fmt.Errorf("generate capability secret: %w", err)
	}
	keyID := id.String()
	token := capabilityPrefix + keyID + "." + base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(token))
	return token, capabilityRecord{
		KeyID:       keyID,
		TokenDigest: hex.EncodeToString(digest[:]),
		Permissions: append([]Permission(nil), permissions...),
		CreatedAt:   now,
	}, nil
}

func capabilityMatches(token string, record capabilityRecord) bool {
	keyID, ok := capabilityKeyID(token)
	if !ok || keyID != record.KeyID {
		return false
	}
	want, err := hex.DecodeString(record.TokenDigest)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

func capabilityKeyID(token string) (string, bool) {
	if !strings.HasPrefix(token, capabilityPrefix) {
		return "", false
	}
	keyID, secret, ok := strings.Cut(strings.TrimPrefix(token, capabilityPrefix), ".")
	if !ok || keyID == "" || secret == "" {
		return "", false
	}
	return keyID, true
}

func hasPermission(record capabilityRecord, permission Permission) bool {
	for _, candidate := range record.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}
