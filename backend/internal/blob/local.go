package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Store interface {
	PutImmutable(tempPath, extension string) (key, digest string, size int64, err error)
	Open(key string) (*os.File, error)
	Delete(key string) error
	Resolve(key string) (string, error)
}

type Local struct {
	root      string
	contentMu sync.Mutex
}

// ContentLease serializes filesystem mutations with the database commit that
// makes them reachable. Cornfield V1 deliberately runs one Worker process, so
// a process-local lease closes the put/reference/delete race without adding a
// distributed lock service. Multi-Worker deployment must replace this with a
// cross-process lease before it is enabled.
type ContentLease struct {
	store    *Local
	released bool
}

func (l *Local) AcquireContentLease() *ContentLease {
	l.contentMu.Lock()
	return &ContentLease{store: l}
}

func (l *ContentLease) Release() {
	if l == nil || l.store == nil || l.released {
		return
	}
	l.released = true
	l.store.contentMu.Unlock()
}

func (l *ContentLease) PutImmutable(tempPath, extension string) (string, string, int64, error) {
	if l == nil || l.store == nil || l.released {
		return "", "", 0, errors.New("content lease is not active")
	}
	return l.store.putImmutable(tempPath, extension)
}

func NewLocal(root string) (*Local, error) {
	if root == "" {
		return nil, errors.New("asset root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{"assets", "uploads/tmp", "uploads/quarantine"} {
		if err := os.MkdirAll(filepath.Join(abs, filepath.FromSlash(dir)), 0o750); err != nil {
			return nil, fmt.Errorf("create storage directory: %w", err)
		}
	}
	return &Local{root: abs}, nil
}

func (l *Local) PutImmutable(tempPath, extension string) (string, string, int64, error) {
	lease := l.AcquireContentLease()
	defer lease.Release()
	return lease.PutImmutable(tempPath, extension)
}

func (l *Local) putImmutable(tempPath, extension string) (string, string, int64, error) {
	f, err := os.OpenFile(tempPath, os.O_RDWR, 0)
	if err != nil {
		return "", "", 0, err
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", "", 0, err
	}
	if closeErr != nil {
		return "", "", 0, closeErr
	}
	digest := hex.EncodeToString(h.Sum(nil))
	extension = strings.ToLower(strings.TrimPrefix(extension, "."))
	if extension == "" || strings.ContainsAny(extension, `/\\`) {
		return "", "", 0, errors.New("invalid asset extension")
	}
	key := filepath.ToSlash(filepath.Join(digest[:2], digest[2:4], digest, "original."+extension))
	destination, err := l.Resolve(key)
	if err != nil {
		return "", "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return "", "", 0, err
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return "", "", 0, errors.New("immutable destination is not a regular file")
		}
		matches, verifyErr := regularFileMatches(destination, digest, size)
		if verifyErr != nil {
			return "", "", 0, fmt.Errorf("verify immutable destination: %w", verifyErr)
		}
		if !matches {
			return "", "", 0, errors.New("immutable destination does not match its content digest")
		}
		// Refresh the file lease before the caller records its DB reference.
		// The conservative orphan sweeper re-checks this timestamp immediately
		// before deletion, closing the content-deduplication reuse window.
		now := time.Now()
		if err := os.Chtimes(destination, now, now); err != nil {
			return "", "", 0, fmt.Errorf("refresh immutable asset lease: %w", err)
		}
		_ = os.Remove(tempPath)
		return key, digest, size, nil
	} else if !os.IsNotExist(err) {
		return "", "", 0, fmt.Errorf("inspect immutable destination: %w", err)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return "", "", 0, fmt.Errorf("commit immutable asset: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return "", "", 0, fmt.Errorf("sync immutable asset directory: %w", err)
	}
	return key, digest, size, nil
}

func regularFileMatches(filename, digest string, size int64) (bool, error) {
	file, err := os.Open(filename)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != size {
		return false, err
	}
	pathInfo, err := os.Lstat(filename)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, pathInfo) {
		return false, err
	}
	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return false, err
	}
	return hex.EncodeToString(hasher.Sum(nil)) == digest, nil
}

func (l *Local) Resolve(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(key))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", errors.New("invalid storage key")
	}
	path := filepath.Join(l.root, "assets", clean)
	assetRoot := filepath.Join(l.root, "assets") + string(os.PathSeparator)
	if !strings.HasPrefix(path+string(os.PathSeparator), assetRoot) {
		return "", errors.New("storage key escapes asset root")
	}
	return path, nil
}

func (l *Local) Open(key string) (*os.File, error) {
	path, err := l.Resolve(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (l *Local) Delete(key string) error {
	path, err := l.Resolve(key)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
