package nodegrpc

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

func ParseRegistrationTokenID(token string) (uint64, error) {
	idPart, secret, _ := strings.Cut(token, ".")
	id, err := strconv.ParseUint(idPart, 10, 32)
	if err != nil || id == 0 || len(secret) != base64.RawURLEncoding.EncodedLen(32) {
		return 0, errors.New("invalid registration token")
	}
	if decoded, err := base64.RawURLEncoding.Strict().DecodeString(secret); err != nil || len(decoded) != 32 {
		return 0, errors.New("invalid registration token")
	}
	return id, nil
}
