package imagex

import (
	"strings"
	"testing"
)

// An upload is checked by content, not by the name the browser gave it.
func TestDecodeRefusesWhatIsNotAnImage(t *testing.T) {
	if _, _, problem := Decode("data:image/gif;base64,R0lGODlh"); problem == "" {
		t.Error("a GIF must be refused")
	}
	// A JPG body behind a PNG prefix: the content decides.
	if _, _, problem := Decode("data:image/png;base64,/9j/4AAQ"); problem == "" {
		t.Error("a mislabelled image must be refused")
	}
	if _, _, problem := Decode("not a data url"); problem == "" {
		t.Error("a plain string must be refused")
	}
}

// The tile shows up to two initials and can never carry markup.
func TestPlaceholder(t *testing.T) {
	svg := string(Placeholder("Gacha <script>", 3))
	if !strings.Contains(svg, ">G</text>") {
		t.Errorf("want one initial, got %s", svg)
	}
	if strings.Contains(svg, "<script") {
		t.Errorf("the name must be escaped: %s", svg)
	}
	if svg := string(Placeholder("gacha ultra rare", 1)); !strings.Contains(svg, ">GU</text>") {
		t.Errorf("want two initials, got %s", svg)
	}
	if svg := string(Placeholder("", 0)); !strings.Contains(svg, ">?</text>") {
		t.Errorf("a nameless product needs a tile too: %s", svg)
	}
}
