package nodegrpc

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

func ParseRegistrationTokenID(token string) (uint64, error) {
	id, _, err := ParseRegistrationToken(token)
	return id, err
}

// ParseRegistrationToken extracts the node ID and optional panel TLS fingerprint.
func ParseRegistrationToken(token string) (uint64, string, error) {
	idPart, suffix, _ := strings.Cut(token, ".")
	secret, fingerprint, pinned := strings.Cut(suffix, ".")
	id, err := strconv.ParseUint(idPart, 10, 32)
	if err != nil || id == 0 || len(secret) != base64.RawURLEncoding.EncodedLen(32) {
		return 0, "", errors.New("invalid registration token")
	}
	if decoded, err := base64.RawURLEncoding.Strict().DecodeString(secret); err != nil || len(decoded) != 32 {
		return 0, "", errors.New("invalid registration token")
	}
	if pinned {
		if _, err := decodeCertificateFingerprint(fingerprint); err != nil {
			return 0, "", errors.New("invalid registration token certificate fingerprint")
		}
	}
	return id, fingerprint, nil
}
