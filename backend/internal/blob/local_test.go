package blob

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImmutablePutAndTraversalProtection(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(root, "uploads", "tmp", "image.part")
	if err := os.WriteFile(temp, []byte("image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, digest, size, err := store.PutImmutable(temp, "png")
	if err != nil || digest == "" || size != 11 {
		t.Fatalf("put failed: key=%q size=%d err=%v", key, size, err)
	}
	if _, err := store.Resolve("../../etc/passwd"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	destination, err := store.Resolve(key)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(destination, old, old); err != nil {
		t.Fatal(err)
	}
	secondTemp := filepath.Join(root, "uploads", "tmp", "second.part")
	if err := os.WriteFile(secondTemp, []byte("image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.PutImmutable(secondTemp, "png"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || !info.ModTime().After(old) {
		t.Fatalf("deduplicated destination lease was not refreshed: %v / %v", info, err)
	}
}

func TestContentLeaseSerializesReferenceCommitAndDeletion(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := store.AcquireContentLease()
	acquired := make(chan *ContentLease, 1)
	go func() { acquired <- store.AcquireContentLease() }()

	select {
	case lease := <-acquired:
		lease.Release()
		t.Fatal("a second filesystem mutation entered before the first database reference commit")
	case <-time.After(50 * time.Millisecond):
	}

	first.Release()
	select {
	case lease := <-acquired:
		lease.Release()
	case <-time.After(time.Second):
		t.Fatal("content lease was not released")
	}
	if _, _, _, err := first.PutImmutable("unused", "png"); err == nil {
		t.Fatal("released content lease accepted a write")
	}
}

func TestImmutablePutRejectsCorruptDeduplicatedDestination(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	firstTemp := filepath.Join(root, "uploads", "tmp", "first.part")
	if err = os.WriteFile(firstTemp, []byte("trusted bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _, _, err := store.PutImmutable(firstTemp, "png")
	if err != nil {
		t.Fatal(err)
	}
	destination, err := store.Resolve(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(destination, []byte("corrupt bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondTemp := filepath.Join(root, "uploads", "tmp", "second-corrupt.part")
	if err = os.WriteFile(secondTemp, []byte("trusted bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = store.PutImmutable(secondTemp, "png"); err == nil {
		t.Fatal("corrupt content-addressed destination was silently reused")
	}
}
