package httpapi

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidFolderName(t *testing.T) {
	for _, test := range []struct {
		name string
		want bool
	}{
		{"Moodboards", true},
		{"灵感 / 夏季", true},
		{"", false},
		{string(make([]rune, 65)), false},
	} {
		if got := validFolderName(test.name); got != test.want {
			t.Fatalf("validFolderName(%q) = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestUniqueBulkAssetIDs(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	values, ok := uniqueBulkAssetIDs([]uuid.UUID{first, first, second})
	if !ok || len(values) != 2 || values[0] != first || values[1] != second {
		t.Fatalf("uniqueBulkAssetIDs = %v, %t", values, ok)
	}
	if _, ok = uniqueBulkAssetIDs(nil); ok {
		t.Fatal("empty bulk request was accepted")
	}
	tooMany := make([]uuid.UUID, maximumBulkAssets+1)
	for index := range tooMany {
		tooMany[index] = uuid.New()
	}
	if _, ok = uniqueBulkAssetIDs(tooMany); ok {
		t.Fatal("oversized bulk request was accepted")
	}
}
