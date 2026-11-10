package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The driver is tested against a real directory, as the rulebook asks for a
// repository: a temp dir stands in for the upload directory.
func TestSaveWritesTheFileAndNeverReusesAName(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)

	url, err := l.Save([]byte("not really a png"), ".png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "/uploads/products/") || !strings.HasSuffix(url, ".png") {
		t.Fatalf("url = %s", url)
	}
	on := filepath.Join(dir, strings.TrimPrefix(url, "/uploads/"))
	b, err := os.ReadFile(on)
	if err != nil {
		t.Fatalf("the file must be where the URL says: %v", err)
	}
	if string(b) != "not really a png" {
		t.Fatalf("content = %q", b)
	}

	again, err := l.Save([]byte("not really a png"), ".png")
	if err != nil {
		t.Fatal(err)
	}
	if again == url {
		t.Fatal("the same bytes must still get a new name, or a cached image would go stale")
	}
}
