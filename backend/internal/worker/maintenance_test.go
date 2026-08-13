package worker

import (
	"strings"
	"testing"
)

func TestBlurBackfillOnlyScansImages(t *testing.T) {
	if !strings.Contains(blurBackfillQuery, "media_type LIKE 'image/%'") {
		t.Fatal("blur backfill query must exclude ZIP packages and other non-image assets")
	}
}
