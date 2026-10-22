package core

import (
	"strings"
	"testing"
	"time"
)

func testService(now time.Time) *Service {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	return New(nil, Options{Location: loc, Now: func() time.Time { return now }})
}

func TestParseRangeDefaultsAndBounds(t *testing.T) {
	// 22 Oct 2026, 01:30 WIB is still 21 Oct in UTC: "today" must follow WIB.
	s := testService(time.Date(2026, 10, 21, 18, 30, 0, 0, time.UTC))
	r, err := s.parseRange("", "", 30, 731)
	if err != nil {
		t.Fatal(err)
	}
	if r.From != "2026-09-23" || r.To != "2026-10-22" {
		t.Fatalf("default range: %s..%s", r.From, r.To)
	}
	if want := time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC); !r.Start.Equal(want) {
		t.Fatalf("start: want %v, got %v", want, r.Start)
	}
	if want := time.Date(2026, 10, 22, 17, 0, 0, 0, time.UTC); !r.End.Equal(want) {
		t.Fatalf("end (exclusive): want %v, got %v", want, r.End)
	}
	for _, bad := range [][2]string{{"2026-10-05", "2026-10-01"}, {"5 Okt", ""}, {"2020-01-01", "2026-01-01"}} {
		if _, err := s.parseRange(bad[0], bad[1], 30, 731); err == nil {
			t.Errorf("%v: want a validation error", bad)
		}
	}
}

func TestPeriods(t *testing.T) {
	s := testService(time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC))
	r, err := s.parseRange("2026-10-01", "2026-10-31", 30, 731)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(periods(r, "day")); n != 31 {
		t.Fatalf("days: %d", n)
	}
	weeks := periods(r, "week")
	if len(weeks) != 5 || weeks[0].key != "2026-09-28" || weeks[4].key != "2026-10-26" {
		t.Fatalf("weeks: %+v", weeks)
	}
	if weeks[0].start.Weekday() != time.Monday {
		t.Fatal("weeks start on Monday")
	}
	months := periods(r, "month")
	if len(months) != 1 || months[0].key != "2026-10" {
		t.Fatalf("months: %+v", months)
	}
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, s.loc) // a Sunday belongs to the week of Monday 19 Oct
	if k := periodKey(day, "week"); k != "2026-10-19" {
		t.Fatalf("week of a Sunday: %s", k)
	}
	if off := utcOffsetSeconds(s.loc, r.Start); off != 7*3600 {
		t.Fatalf("WIB offset: %d", off)
	}
}

func TestChangedFields(t *testing.T) {
	b, a, changed := changedFields(
		map[string]any{"name": "A", "price": int64(25000), "image": "x"},
		map[string]any{"name": "A", "price": int64(30000), "image": "x"})
	if !changed || len(a) != 1 || a["price"] != int64(30000) || b["price"] != int64(25000) {
		t.Fatalf("got %v -> %v", b, a)
	}
	if _, _, changed := changedFields(map[string]any{"x": nil}, map[string]any{"x": nil}); changed {
		t.Fatal("nil to nil is no change")
	}
}

func TestDecodeImage(t *testing.T) {
	if _, _, msg := decodeImage("data:image/gif;base64,R0lGODlh"); msg == "" {
		t.Fatal("gif must be refused")
	}
	// A JPEG header inside a PNG data URL is judged by content and refused as unreadable.
	if _, _, msg := decodeImage("data:image/png;base64,/9j/4AAQ"); msg == "" {
		t.Fatal("a truncated image must be refused")
	}
	// Initials come from letters and digits only, so markup in a name never reaches the SVG.
	if svg := string(placeholderSVG("Gacha <script>", 3)); !strings.Contains(svg, ">G</text>") || strings.Contains(svg, "<script") {
		t.Fatalf("placeholder: %s", svg)
	}
	if svg := string(placeholderSVG("gacha ultra rare", 1)); !strings.Contains(svg, ">GU</text>") {
		t.Fatalf("two initials: %s", svg)
	}
}
