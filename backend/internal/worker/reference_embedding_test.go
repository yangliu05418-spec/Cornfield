package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"internal-image-studio/internal/blob"
)

func TestEmbedGenerationReferencesUsesBoundedDataURLs(t *testing.T) {
	root := t.TempDir()
	store, err := blob.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(root, "reference.part")
	if err := os.WriteFile(temporary, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _, size, err := store.PutImmutable(temporary, "png")
	if err != nil {
		t.Fatal(err)
	}
	worker := &GenerateWorker{Blobs: store}
	values, err := worker.embedGenerationReferences([]generationReference{{
		ID: uuid.New(), StorageKey: key, MediaType: "image/png", ByteSize: size,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || !strings.HasPrefix(values[0], "data:image/png;base64,") {
		t.Fatalf("embedded references = %#v", values)
	}
}

func TestEmbeddedReferenceBytesRejectsOversizedCombinedPayload(t *testing.T) {
	references := []generationReference{
		{ByteSize: bytePlusEmbeddedReferenceLimit},
		{ByteSize: 1},
	}
	if got := embeddedReferenceBytes(references); got <= bytePlusEmbeddedReferenceLimit {
		t.Fatalf("combined bytes = %d", got)
	}
}
