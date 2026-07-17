package httpapi

import (
	"path/filepath"

	"internal-image-studio/internal/diskspace"
)

// The API receives a read-only asset tree and a nested writable uploads mount.
// Both are required to live on the same host filesystem, so the writable path
// is the correct capacity and read-only-state probe for generation admission.
func storageFreePercent(assetRoot string) (float64, error) {
	return diskspace.FreePercent(filepath.Join(assetRoot, "uploads"))
}
