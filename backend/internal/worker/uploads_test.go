package worker

import "testing"

func TestCanonicalUploadFilenameUsesDecodedExtension(t *testing.T) {
	tests := []struct {
		filename  string
		extension string
		want      string
	}{
		{filename: "photo.png", extension: ".jpg", want: "photo.jpg"},
		{filename: "reference", extension: ".webp", want: "reference.webp"},
		{filename: ".jpeg", extension: ".png", want: "image.png"},
	}
	for _, test := range tests {
		if got := canonicalUploadFilename(test.filename, test.extension); got != test.want {
			t.Errorf("canonicalUploadFilename(%q, %q) = %q; want %q", test.filename, test.extension, got, test.want)
		}
	}
}

func TestReferenceUploadsStayOutOfLibrary(t *testing.T) {
	if uploadVisibleInLibrary("reference") {
		t.Fatal("reference upload must not be visible in the asset library")
	}
	for _, purpose := range []string{"library", ""} {
		if !uploadVisibleInLibrary(purpose) {
			t.Fatalf("upload purpose %q must remain visible in the asset library", purpose)
		}
	}
}
