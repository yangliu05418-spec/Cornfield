package providerurl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const PathPrefix = "/api/v1/provider-assets/"

var (
	ErrExpired          = errors.New("provider asset URL expired")
	ErrInvalidSignature = errors.New("provider asset URL signature invalid")
)

func Sign(publicURL, secret string, assetID uuid.UUID, extension string, expiresAt time.Time) (string, error) {
	base, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("invalid public URL")
	}
	extension = normalizeExtension(extension)
	expires := expiresAt.UTC().Unix()
	path := PathPrefix + assetID.String() + "/reference" + extension
	query := base64.RawURLEncoding.EncodeToString(signature(secret, assetID, path, expires))
	base.Path = path
	base.RawQuery = "expires=" + strconv.FormatInt(expires, 10) + "&signature=" + url.QueryEscape(query)
	return base.String(), nil
}

func Verify(secret string, assetID uuid.UUID, path, expiresRaw, signatureRaw string, now time.Time) error {
	expires, err := strconv.ParseInt(expiresRaw, 10, 64)
	if err != nil || expires <= now.UTC().Unix() {
		return ErrExpired
	}
	provided, err := base64.RawURLEncoding.DecodeString(signatureRaw)
	if err != nil {
		return ErrInvalidSignature
	}
	expected := signature(secret, assetID, path, expires)
	if !hmac.Equal(provided, expected) {
		return ErrInvalidSignature
	}
	return nil
}

func signature(secret string, assetID uuid.UUID, path string, expires int64) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(assetID.String()))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(path))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(strconv.FormatInt(expires, 10)))
	return mac.Sum(nil)
}

func normalizeExtension(extension string) string {
	extension = strings.ToLower(strings.TrimSpace(extension))
	if !strings.HasPrefix(extension, ".") {
		extension = "." + extension
	}
	switch extension {
	case ".jpg", ".jpeg", ".png", ".webp":
		return extension
	default:
		return ".img"
	}
}
