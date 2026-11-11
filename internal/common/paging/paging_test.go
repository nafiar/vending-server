package paging_test

import (
	"testing"

	"vending/internal/common/paging"
)

func TestOfClamps(t *testing.T) {
	cases := []struct {
		name               string
		number, size, def  int
		wantNumber, wantSz int
	}{
		{"defaults", 0, 0, 25, 1, 25},
		{"negative page is the first", -3, 10, 25, 1, 10},
		{"size above the ceiling is capped", 2, 5000, 25, 2, paging.MaxSize},
		{"asked for what it may have", 3, 50, 25, 3, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := paging.Of(c.number, c.size, c.def)
			if p.Number != c.wantNumber || p.Size != c.wantSz {
				t.Fatalf("Of(%d,%d,%d) = %+v, want {%d %d}", c.number, c.size, c.def, p, c.wantNumber, c.wantSz)
			}
		})
	}
}

func TestWindow(t *testing.T) {
	p := paging.Of(3, 20, 25)
	if limit, offset := p.Window(false); limit != 20 || offset != 40 {
		t.Fatalf("page 3 of 20: limit=%d offset=%d, want 20 and 40", limit, offset)
	}
	// An export ignores the page: it wants every matching row from the first.
	if limit, offset := p.Window(true); limit != paging.MaxExportRows || offset != 0 {
		t.Fatalf("export: limit=%d offset=%d, want %d and 0", limit, offset, paging.MaxExportRows)
	}
}
