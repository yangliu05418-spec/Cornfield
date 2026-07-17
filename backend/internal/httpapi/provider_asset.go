package httpapi

import (
	"net/http"
	"time"

	"internal-image-studio/internal/providerurl"
)

func (s *Server) providerAsset(w http.ResponseWriter, r *http.Request) {
	assetID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	expires := r.URL.Query().Get("expires")
	signature := r.URL.Query().Get("signature")
	if s.cfg.ProviderURLSigningSecret == "" || providerurl.Verify(s.cfg.ProviderURLSigningSecret, assetID, r.URL.Path, expires, signature, time.Now()) != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var storageKey, mediaType string
	err := s.db.QueryRow(r.Context(), `SELECT storage_key,media_type FROM assets
		WHERE id=$1 AND purged_at IS NULL AND purge_pending=false`, assetID).Scan(&storageKey, &mediaType)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Accel-Redirect", "/_protected_assets/"+storageKey)
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.WriteHeader(http.StatusOK)
}
