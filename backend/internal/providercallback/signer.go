package providercallback

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

const PathPrefix = "/api/v1/provider-callbacks/legnext/"

func URL(publicURL, secret string, jobID uuid.UUID) (string, error) {
	base, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("invalid public URL")
	}
	signature := base64.RawURLEncoding.EncodeToString(sign(secret, jobID))
	base.Path = PathPrefix + jobID.String() + "/" + signature
	base.RawQuery = ""
	return base.String(), nil
}

func Verify(secret string, jobID uuid.UUID, encoded string) bool {
	provided, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	return hmac.Equal(provided, sign(secret, jobID))
}

func sign(secret string, jobID uuid.UUID) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("legnext-callback\n"))
	mac.Write([]byte(jobID.String()))
	return mac.Sum(nil)
}
