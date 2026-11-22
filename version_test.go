package mybuilds

import (
	"os"
	"strings"
	"testing"
)

func TestShippedVersionIsSemantic(t *testing.T) {
	raw, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Version, strings.TrimSpace(string(raw)); got != want {
		t.Fatalf("Version = %q, VERSION holds %q", got, want)
	}
	if !semver.MatchString(Version) {
		t.Fatalf("Version %q is not a semantic version", Version)
	}
}

func TestParseVersion(t *testing.T) {
	for _, text := range []string{"1.11.0\n", "1.11.0\r\n", "2.0.0-rc.1", "1.11.0+sha.0123456"} {
		if _, err := ParseVersion(text); err != nil {
			t.Errorf("ParseVersion(%q): %v", text, err)
		}
	}
	for _, text := range []string{"", "dev", "1.11", "v1.11.0", "01.11.0", "1.11.0.1", "1.11.0 beta", "sha-0123456"} {
		if _, err := ParseVersion(text); err == nil {
			t.Errorf("ParseVersion(%q) accepted a malformed version", text)
		}
	}
}
