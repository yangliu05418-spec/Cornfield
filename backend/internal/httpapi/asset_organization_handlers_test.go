package httpapi

import "testing"

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
