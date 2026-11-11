package core

import (
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
