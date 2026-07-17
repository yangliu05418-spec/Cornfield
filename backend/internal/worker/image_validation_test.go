package worker

import (
	"encoding/hex"
	"testing"
)

func TestValidateProviderImageRejectsHeaderOnlyPNG(t *testing.T) {
	// Valid PNG signature and IHDR for a 1x1 RGBA image, deliberately missing
	// IDAT and IEND. image.DecodeConfig accepts it; a complete decode must not.
	data, err := hex.DecodeString("89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = validateImage(data); err != nil {
		t.Fatalf("test fixture no longer reaches the full-decode boundary: %v", err)
	}
	if _, _, _, _, err = validateProviderImage(data); err == nil {
		t.Fatal("header-only PNG passed provider full-decode validation")
	}
}
