package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContentStorageKeyValidation(t *testing.T) {
	digest := digestFor([]byte("valid"))
	key := filepath.ToSlash(filepath.Join(digest[:2], digest[2:4], digest, "original.png"))
	if _, err := parseContentStorageKey(key, digest); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	invalid := []struct {
		key    string
		digest string
	}{
		{key: "../" + key, digest: digest},
		{key: key, digest: digest[:63] + "A"},
		{key: filepath.ToSlash(filepath.Join("ff", digest[2:4], digest, "original.png")), digest: digest},
		{key: filepath.ToSlash(filepath.Join(digest[:2], digest[2:4], digest, "other.png")), digest: digest},
	}
	for _, test := range invalid {
		if _, err := parseContentStorageKey(test.key, test.digest); err == nil {
			t.Fatalf("invalid key accepted: %q / %q", test.key, test.digest)
		}
	}
}

func TestOrphanScanAndDeleteIsConservative(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	orphanDigest, orphanKey, orphanDir := createContentFixture(t, root, []byte("orphan"), now.Add(-25*time.Hour), false)
	writeOldFile(t, filepath.Join(orphanDir, "thumb-320.webp"), []byte("thumb"), now.Add(-25*time.Hour))
	writeOldFile(t, filepath.Join(orphanDir, "thumb-640.webp.part"), []byte("partial"), now.Add(-25*time.Hour))
	setOldTime(t, orphanDir, now.Add(-25*time.Hour))

	referencedDigest, referencedKey, referencedDir := createContentFixture(t, root, []byte("referenced"), now.Add(-25*time.Hour), false)
	_, _, recentDir := createContentFixture(t, root, []byte("recent"), now.Add(-time.Hour), false)
	mismatchDigest, _, mismatchDir := createContentFixture(t, root, []byte("declared digest"), now.Add(-25*time.Hour), false)
	if err := os.WriteFile(filepath.Join(mismatchDir, "original.png"), []byte("different bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	setOldTime(t, filepath.Join(mismatchDir, "original.png"), now.Add(-25*time.Hour))
	setOldTime(t, mismatchDir, now.Add(-25*time.Hour))
	_, _, unknownDir := createContentFixture(t, root, []byte("unknown-file"), now.Add(-25*time.Hour), false)
	writeOldFile(t, filepath.Join(unknownDir, "notes.txt"), []byte("do not delete"), now.Add(-25*time.Hour))
	setOldTime(t, unknownDir, now.Add(-25*time.Hour))

	references := map[string]storageReference{}
	if err := addStorageReference(references, referencedKey, referencedDigest, true); err != nil {
		t.Fatal(err)
	}
	candidates, stats, err := scanOrphanCandidates(context.Background(), root, references, now, 24*time.Hour, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned < 5 || len(candidates) != 1 || candidates[0].Digest != orphanDigest || candidates[0].StorageKey != orphanKey {
		t.Fatalf("candidates=%+v stats=%+v mismatch=%s", candidates, stats, mismatchDigest)
	}
	deletedBytes, err := deleteOrphanCandidate(candidates[0], now, 24*time.Hour)
	if err != nil || deletedBytes <= int64(len("orphan")) {
		t.Fatalf("deleteOrphanCandidate bytes=%d err=%v", deletedBytes, err)
	}
	if _, err := os.Lstat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("orphan directory still exists: %v", err)
	}
	for _, protected := range []string{referencedDir, recentDir, mismatchDir, unknownDir} {
		if _, err := os.Lstat(protected); err != nil {
			t.Fatalf("protected directory was removed: %s: %v", protected, err)
		}
	}
}

func TestOrphanScanHonorsCandidateLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, content := range []string{"one", "two", "three"} {
		createContentFixture(t, root, []byte(content), now.Add(-25*time.Hour), false)
	}
	candidates, _, err := scanOrphanCandidates(context.Background(), root, map[string]storageReference{}, now, 24*time.Hour, 100, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%d err=%v", len(candidates), err)
	}
}

func TestOrphanDeleteRefusesRecentlyLeasedContent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, _, directory := createContentFixture(t, root, []byte("leased while sweeping"), now.Add(-25*time.Hour), false)
	candidates, _, err := scanOrphanCandidates(context.Background(), root, map[string]storageReference{}, now, 24*time.Hour, 100, 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%d err=%v", len(candidates), err)
	}
	original := filepath.Join(directory, "original.png")
	if err := os.Chtimes(original, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteOrphanCandidate(candidates[0], now, 24*time.Hour); !errors.Is(err, errOrphanCandidateChanged) {
		t.Fatalf("recently leased content was not rejected: %v", err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("recently leased content was removed: %v", err)
	}
}

func TestExpiryDeleteHonorsContentReuseLease(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	digest, key, directory := createContentFixture(t, root, []byte("deduplicated reuse"), now.Add(-time.Hour), false)
	original := filepath.Join(directory, "original.png")
	if err := os.Chtimes(original, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteCanonicalContent(root, key, digest, now); !errors.Is(err, errOrphanCandidateChanged) {
		t.Fatalf("recent content lease was not honored: %v", err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("recently reused content was removed: %v", err)
	}

	old := now.Add(-defaultContentReuseLease - time.Minute)
	setOldTime(t, original, old)
	setOldTime(t, directory, old)
	if _, err := deleteCanonicalContent(root, key, digest, now); err != nil {
		t.Fatalf("expired unreferenced content was not removed after lease: %v", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("expired content directory still exists: %v", err)
	}
}

func TestOrphanScanDoesNotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	assetsRoot := filepath.Join(root, "assets")
	if err := os.MkdirAll(assetsRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	digest := digestFor([]byte("outside"))
	outsideFirst := filepath.Join(outside, digest[:2])
	directory := filepath.Join(outsideFirst, digest[2:4], digest)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	writeOldFile(t, filepath.Join(directory, "original.png"), []byte("outside"), time.Now().Add(-25*time.Hour))
	if err := os.Symlink(outsideFirst, filepath.Join(assetsRoot, digest[:2])); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	candidates, _, err := scanOrphanCandidates(context.Background(), root, map[string]storageReference{}, time.Now(), 24*time.Hour, 100, 10)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("symlink produced candidates=%+v err=%v", candidates, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "original.png")); err != nil {
		t.Fatalf("symlink target was modified: %v", err)
	}
}

func TestMissingThumbnailDetectionIsBoundedAndSafe(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	missingDigest, missingKey, _ := createContentFixture(t, root, []byte("missing"), now, false)
	completeDigest, completeKey, completeDir := createContentFixture(t, root, []byte("complete"), now, false)
	for _, name := range []string{"thumb-320.webp", "thumb-640.webp", "thumb-1280.webp"} {
		writeOldFile(t, filepath.Join(completeDir, name), []byte("thumb"), now)
	}
	unsafeDigest, unsafeKey, unsafeDir := createContentFixture(t, root, []byte("unsafe"), now, false)
	if err := os.Mkdir(filepath.Join(unsafeDir, "thumb-320.webp"), 0o750); err != nil {
		t.Fatal(err)
	}
	references := map[string]storageReference{}
	for _, item := range []struct {
		digest string
		key    string
	}{
		{missingDigest, missingKey},
		{completeDigest, completeKey},
		{unsafeDigest, unsafeKey},
	} {
		if err := addStorageReference(references, item.key, item.digest, true); err != nil {
			t.Fatal(err)
		}
	}
	keys, scanned, unsafe := findMissingThumbnailKeys(root, references, 10, 10)
	if scanned != 3 || unsafe != 1 || len(keys) != 1 || keys[0] != missingKey {
		t.Fatalf("keys=%v scanned=%d unsafe=%d", keys, scanned, unsafe)
	}
	stagedOnly := map[string]storageReference{missingDigest: {StorageKey: missingKey}}
	if keys, _, _ := findMissingThumbnailKeys(root, stagedOnly, 10, 10); len(keys) != 0 {
		t.Fatalf("staged-only output scheduled thumbnail repair: %v", keys)
	}
}

func createContentFixture(t *testing.T, root string, content []byte, modTime time.Time, completeThumbs bool) (string, string, string) {
	t.Helper()
	digest := digestFor(content)
	directory := filepath.Join(root, "assets", digest[:2], digest[2:4], digest)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	writeOldFile(t, filepath.Join(directory, "original.png"), content, modTime)
	if completeThumbs {
		for _, name := range []string{"thumb-320.webp", "thumb-640.webp", "thumb-1280.webp"} {
			writeOldFile(t, filepath.Join(directory, name), []byte("thumb"), modTime)
		}
	}
	setOldTime(t, directory, modTime)
	key := filepath.ToSlash(filepath.Join(digest[:2], digest[2:4], digest, "original.png"))
	return digest, key, directory
}

func writeOldFile(t *testing.T, filename string, content []byte, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(filename, content, 0o640); err != nil {
		t.Fatal(err)
	}
	setOldTime(t, filename, modTime)
}

func setOldTime(t *testing.T, filename string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(filename, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func digestFor(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
