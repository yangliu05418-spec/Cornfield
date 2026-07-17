package providercallback

import (
	"net/url"
	"path"
	"testing"

	"github.com/google/uuid"
)

func TestURLAndVerify(t *testing.T) {
	id := uuid.MustParse("e716a8a2-c1db-4881-9bd0-7aaa7208b55a")
	callback, err := URL("https://studio.example", "callback-secret", id)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(callback)
	if !Verify("callback-secret", id, path.Base(parsed.Path)) {
		t.Fatal("expected callback signature to verify")
	}
	if Verify("wrong-secret", id, path.Base(parsed.Path)) {
		t.Fatal("expected wrong secret to fail")
	}
}
