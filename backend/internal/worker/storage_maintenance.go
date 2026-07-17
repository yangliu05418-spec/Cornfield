package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultOrphanMinAge         = 24 * time.Hour
	defaultOrphanScanLimit      = 5000
	defaultOrphanDeleteLimit    = 100
	defaultThumbnailScanLimit   = 2000
	defaultThumbnailRepairLimit = 25
	maxMaintainedOriginalBytes  = 50 << 20
	// PutImmutable refreshes the original file timestamp before recording a
	// deduplicated database reference. Expiry deletion must honor that lease so
	// it cannot remove content between the filesystem commit and DB commit.
	defaultContentReuseLease = 5 * time.Minute
)

var errOrphanCandidateChanged = errors.New("orphan candidate changed during sweep")

type storageReference struct {
	StorageKey  string
	ActiveAsset bool
}

type orphanCandidate struct {
	Digest     string
	StorageKey string
	Directory  string
	Files      []string
	ByteSize   int64
}

type orphanScanStats struct {
	Scanned int
	Skipped int
}

func addStorageReference(references map[string]storageReference, storageKey, digest string, activeAsset bool) error {
	parts, err := parseContentStorageKey(storageKey, digest)
	if err != nil {
		return err
	}
	canonicalKey := strings.Join(parts, "/")
	if existing, ok := references[digest]; ok {
		if existing.StorageKey != canonicalKey {
			return fmt.Errorf("digest %s has conflicting storage keys", digest)
		}
		existing.ActiveAsset = existing.ActiveAsset || activeAsset
		references[digest] = existing
		return nil
	}
	references[digest] = storageReference{StorageKey: canonicalKey, ActiveAsset: activeAsset}
	return nil
}

func parseContentStorageKey(storageKey, digest string) ([]string, error) {
	if len(digest) != 64 || !isLowerHex(digest) || strings.Contains(storageKey, "\\") || path.Clean(storageKey) != storageKey {
		return nil, errors.New("invalid content-addressed storage reference")
	}
	parts := strings.Split(storageKey, "/")
	if len(parts) != 4 || parts[0] != digest[:2] || parts[1] != digest[2:4] || parts[2] != digest || !isOriginalFilename(parts[3]) {
		return nil, errors.New("storage key does not match its sha256 digest")
	}
	return parts, nil
}

func scanOrphanCandidates(ctx context.Context, assetRoot string, references map[string]storageReference, now time.Time, minAge time.Duration, scanLimit, candidateLimit int) ([]orphanCandidate, orphanScanStats, error) {
	stats := orphanScanStats{}
	if minAge <= 0 || scanLimit < 1 || candidateLimit < 1 {
		return nil, stats, errors.New("invalid orphan scan limits")
	}
	assetsRoot, err := safeAssetsRoot(assetRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []orphanCandidate{}, stats, nil
		}
		return nil, stats, err
	}
	firstLevel, err := os.ReadDir(assetsRoot)
	if err != nil {
		return nil, stats, err
	}
	candidates := make([]orphanCandidate, 0, candidateLimit)
	cutoff := now.Add(-minAge)
	examined := 0
	for _, first := range firstLevel {
		examined++
		if examined > scanLimit {
			return candidates, stats, nil
		}
		if ctx.Err() != nil {
			return candidates, stats, ctx.Err()
		}
		if !safeHexDirectoryEntry(first, 2) {
			stats.Skipped++
			continue
		}
		firstPath, safeErr := safeContentDirectory(assetRoot, []string{first.Name()})
		if safeErr != nil {
			stats.Skipped++
			continue
		}
		secondLevel, readErr := os.ReadDir(firstPath)
		if readErr != nil {
			stats.Skipped++
			continue
		}
		for _, second := range secondLevel {
			examined++
			if examined > scanLimit {
				return candidates, stats, nil
			}
			if !safeHexDirectoryEntry(second, 2) {
				stats.Skipped++
				continue
			}
			secondPath, safeErr := safeContentDirectory(assetRoot, []string{first.Name(), second.Name()})
			if safeErr != nil {
				stats.Skipped++
				continue
			}
			digestLevel, readErr := os.ReadDir(secondPath)
			if readErr != nil {
				stats.Skipped++
				continue
			}
			for _, digestEntry := range digestLevel {
				examined++
				if ctx.Err() != nil {
					return candidates, stats, ctx.Err()
				}
				if examined > scanLimit || len(candidates) >= candidateLimit {
					return candidates, stats, nil
				}
				digest := digestEntry.Name()
				if !safeHexDirectoryEntry(digestEntry, 64) || digest[:2] != first.Name() || digest[2:4] != second.Name() {
					stats.Skipped++
					continue
				}
				stats.Scanned++
				if _, referenced := references[digest]; referenced {
					continue
				}
				directory, safeErr := safeContentDirectory(assetRoot, []string{first.Name(), second.Name(), digest})
				if safeErr != nil {
					stats.Skipped++
					continue
				}
				candidate, ok, inspectErr := inspectContentDirectory(directory, digest, cutoff)
				if inspectErr != nil || !ok {
					stats.Skipped++
					continue
				}
				candidate.StorageKey = path.Join(first.Name(), second.Name(), digest, filepath.Base(candidate.Files[len(candidate.Files)-1]))
				candidates = append(candidates, candidate)
			}
		}
	}
	return candidates, stats, nil
}

func inspectContentDirectory(directory, digest string, cutoff time.Time) (orphanCandidate, bool, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return orphanCandidate{}, false, nil
		}
		return orphanCandidate{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.ModTime().After(cutoff) {
		return orphanCandidate{}, false, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return orphanCandidate{}, false, err
	}
	files := make([]string, 0, 7)
	var originalPath string
	var byteSize int64
	for _, entry := range entries {
		name := entry.Name()
		if !isOriginalFilename(name) && !isThumbnailFilename(name) {
			return orphanCandidate{}, false, nil
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return orphanCandidate{}, false, nil
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil || !entryInfo.Mode().IsRegular() || entryInfo.ModTime().After(cutoff) {
			return orphanCandidate{}, false, nil
		}
		fullPath := filepath.Join(directory, name)
		byteSize += entryInfo.Size()
		if isOriginalFilename(name) {
			if entryInfo.Size() > maxMaintainedOriginalBytes {
				return orphanCandidate{}, false, nil
			}
			if originalPath != "" {
				return orphanCandidate{}, false, nil
			}
			originalPath = fullPath
			continue
		}
		files = append(files, fullPath)
	}
	if originalPath == "" {
		return orphanCandidate{}, false, nil
	}
	matches, err := fileMatchesDigest(originalPath, digest)
	if err != nil || !matches {
		return orphanCandidate{}, false, err
	}
	// Delete derivatives first and the original last so an interrupted sweep
	// remains retryable on the next pass.
	files = append(files, originalPath)
	return orphanCandidate{Digest: digest, Directory: directory, Files: files, ByteSize: byteSize}, true, nil
}

func deleteOrphanCandidate(candidate orphanCandidate, now time.Time, minAge time.Duration) (int64, error) {
	verified, ok, err := inspectContentDirectory(candidate.Directory, candidate.Digest, now.Add(-minAge))
	if err != nil {
		return 0, err
	}
	if !ok || filepath.Base(verified.Files[len(verified.Files)-1]) != filepath.Base(candidate.Files[len(candidate.Files)-1]) {
		return 0, errOrphanCandidateChanged
	}
	return removeVerifiedContent(verified)
}

func deleteCanonicalContent(assetRoot, storageKey, digest string, now time.Time) (int64, error) {
	parts, err := parseContentStorageKey(storageKey, digest)
	if err != nil {
		return 0, err
	}
	directory, err := safeContentDirectory(assetRoot, parts[:3])
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	verified, ok, err := inspectContentDirectory(directory, digest, now.Add(-defaultContentReuseLease))
	if err != nil {
		return 0, err
	}
	if !ok || filepath.Base(verified.Files[len(verified.Files)-1]) != parts[3] {
		return 0, errOrphanCandidateChanged
	}
	return removeVerifiedContent(verified)
}

func removeVerifiedContent(verified orphanCandidate) (int64, error) {
	for _, filename := range verified.Files {
		if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	}
	if err := os.Remove(verified.Directory); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	// Remove empty fan-out directories only; os.Remove never removes non-empty
	// directories and therefore cannot erase adjacent content.
	second := filepath.Dir(verified.Directory)
	first := filepath.Dir(second)
	_ = os.Remove(second)
	_ = os.Remove(first)
	return verified.ByteSize, nil
}

func findMissingThumbnailKeys(assetRoot string, references map[string]storageReference, scanLimit, repairLimit int) ([]string, int, int) {
	keys := make([]string, 0, repairLimit)
	if scanLimit < 1 || repairLimit < 1 {
		return keys, 0, 0
	}
	scanned, unsafe, examined := 0, 0, 0
	for digest, reference := range references {
		if examined >= scanLimit || len(keys) >= repairLimit {
			break
		}
		examined++
		if !reference.ActiveAsset {
			continue
		}
		scanned++
		parts, err := parseContentStorageKey(reference.StorageKey, digest)
		if err != nil {
			unsafe++
			continue
		}
		directory, err := safeContentDirectory(assetRoot, parts[:3])
		if err != nil {
			unsafe++
			continue
		}
		original := filepath.Join(directory, parts[3])
		info, err := os.Lstat(original)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxMaintainedOriginalBytes {
			unsafe++
			continue
		}
		missing, safe := false, true
		for _, name := range []string{"thumb-320.webp", "thumb-640.webp", "thumb-1280.webp"} {
			thumbInfo, statErr := os.Lstat(filepath.Join(directory, name))
			if os.IsNotExist(statErr) {
				missing = true
				continue
			}
			if statErr != nil || thumbInfo.Mode()&os.ModeSymlink != 0 || !thumbInfo.Mode().IsRegular() {
				safe = false
				break
			}
		}
		if !safe {
			unsafe++
			continue
		}
		if missing {
			matches, matchErr := fileMatchesDigest(original, digest)
			if matchErr != nil || !matches {
				unsafe++
				continue
			}
			keys = append(keys, reference.StorageKey)
		}
	}
	return keys, scanned, unsafe
}

func safeAssetsRoot(assetRoot string) (string, error) {
	root, err := filepath.Abs(filepath.Join(assetRoot, "assets"))
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("asset directory is not a safe directory")
	}
	return root, nil
}

func safeContentDirectory(assetRoot string, components []string) (string, error) {
	current, err := safeAssetsRoot(assetRoot)
	if err != nil {
		return "", err
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." || filepath.Base(component) != component {
			return "", errors.New("invalid content directory component")
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("content path contains an unsafe directory")
		}
	}
	return current, nil
}

func fileMatchesDigest(filename, digest string) (bool, error) {
	file, err := os.Open(filename)
	if err != nil {
		return false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		return false, err
	}
	pathInfo, err := os.Lstat(filename)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		return false, err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return false, err
	}
	return hex.EncodeToString(hasher.Sum(nil)) == digest, nil
}

func safeHexDirectoryEntry(entry os.DirEntry, length int) bool {
	return entry.Type()&os.ModeSymlink == 0 && entry.IsDir() && len(entry.Name()) == length && isLowerHex(entry.Name())
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return value != ""
}

func isOriginalFilename(name string) bool {
	switch name {
	case "original.jpg", "original.png", "original.webp":
		return true
	default:
		return false
	}
}

func isThumbnailFilename(name string) bool {
	return name == "thumb-320.webp" || name == "thumb-640.webp" || name == "thumb-1280.webp" ||
		name == "thumb-320.webp.part" || name == "thumb-640.webp.part" || name == "thumb-1280.webp.part"
}
