package calendar_test

import (
	"testing"
	"time"

	"vending/internal/common/calendar"
	"vending/internal/model"
)

var jakarta = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		panic(err)
	}
	return loc
}()

// A day is a day in a place. The whole point of this package is that "today"
// follows Asia/Jakarta and not the UTC the database stores, so the case worth
// pinning down is the one where the two disagree.
func TestParseRangeFollowsTheLocalDayNotUTC(t *testing.T) {
	// 22 Oct 2026 01:30 WIB is still 21 Oct in UTC.
	now := time.Date(2026, 10, 21, 18, 30, 0, 0, time.UTC)
	r, err := calendar.ParseRange(now, jakarta, "", "", 30, 731)
	if err != nil {
		t.Fatal(err)
	}
	if r.From != "2026-09-23" || r.To != "2026-10-22" {
		t.Fatalf("range = %s..%s, want it to end on the local day 2026-10-22", r.From, r.To)
	}
	// The bounds are half-open, which is what makes the last day whole without
	// anyone writing 23:59:59.999.
	if want := time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC); !r.Start.Equal(want) {
		t.Errorf("start = %v, want %v", r.Start, want)
	}
	if want := time.Date(2026, 10, 22, 17, 0, 0, 0, time.UTC); !r.End.Equal(want) {
		t.Errorf("end = %v, want %v (exclusive)", r.End, want)
	}
}

func TestParseRangeRefusesWhatItCannotUse(t *testing.T) {
	now := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		from  string
		to    string
		field string
	}{
		{"the start is after the end", "2026-10-05", "2026-10-01", "from"},
		{"the start is not a date", "5 Okt", "", "from"},
		{"the end is not a date", "", "31 Des", "to"},
		{"the span is longer than allowed", "2020-01-01", "2026-01-01", "from"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := calendar.ParseRange(now, jakarta, tc.from, tc.to, 30, 731)
			e, ok := model.AsError(err)
			if !ok {
				t.Fatalf("err = %v, want a model.Error", err)
			}
			if e.Code != model.CodeValidation {
				t.Errorf("code = %s, want %s", e.Code, model.CodeValidation)
			}
			if e.Fields[tc.field] == "" {
				t.Errorf("fields = %v, want %s named", e.Fields, tc.field)
			}
		})
	}
}

// Both dates are reported on when both are wrong, so a form marks both rather
// than making the admin fix one and resubmit to find the other.
func TestParseRangeReportsBothDatesAtOnce(t *testing.T) {
	now := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	_, err := calendar.ParseRange(now, jakarta, "bukan", "juga-bukan", 30, 731)
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	if e.Fields["from"] == "" || e.Fields["to"] == "" {
		t.Errorf("fields = %v, want both named", e.Fields)
	}
}

// Exactly maxDays is allowed: it is the limit, not past it.
func TestParseRangeAllowsExactlyTheLimit(t *testing.T) {
	now := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	if _, err := calendar.ParseRange(now, jakarta, "2026-10-01", "2026-10-31", 30, 31); err != nil {
		t.Errorf("31 days with a 31-day limit should be allowed: %v", err)
	}
	if _, err := calendar.ParseRange(now, jakarta, "2026-10-01", "2026-10-31", 30, 30); err == nil {
		t.Error("31 days with a 30-day limit should be refused")
	}
}

// Every bucket overlapping the range is listed, empty ones included, or a quiet
// week would make the chart look like a short one.
func TestPeriodsCoversTheWholeRangeIncludingEmptyBuckets(t *testing.T) {
	now := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	r, err := calendar.ParseRange(now, jakarta, "2026-10-01", "2026-10-31", 30, 731)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(calendar.Periods(r, "day")); n != 31 {
		t.Errorf("days = %d, want 31", n)
	}
	weeks := calendar.Periods(r, "week")
	if len(weeks) != 5 {
		t.Fatalf("weeks = %d, want 5", len(weeks))
	}
	// The first week starts before the range does: 1 Oct is a Thursday, so its
	// week began on Monday 28 Sep.
	if weeks[0].Key != "2026-09-28" || weeks[4].Key != "2026-10-26" {
		t.Errorf("weeks = %s..%s", weeks[0].Key, weeks[4].Key)
	}
	for _, w := range weeks {
		if w.Start.Weekday() != time.Monday {
			t.Errorf("week %s starts on %s, want Monday", w.Key, w.Start.Weekday())
		}
	}
	months := calendar.Periods(r, "month")
	if len(months) != 1 || months[0].Key != "2026-10" {
		t.Errorf("months = %+v, want one bucket 2026-10", months)
	}
}

// ISO weeks run Monday to Sunday, so a Sunday belongs to the week that began
// six days earlier, not to the one starting the next day.
func TestPeriodKeyPutsASundayInTheWeekThatBegan(t *testing.T) {
	sunday := time.Date(2026, 10, 25, 0, 0, 0, 0, jakarta)
	if sunday.Weekday() != time.Sunday {
		t.Fatalf("the fixture is wrong: %s is a %s", sunday, sunday.Weekday())
	}
	if k := calendar.PeriodKey(sunday, "week"); k != "2026-10-19" {
		t.Errorf("week of a Sunday = %s, want 2026-10-19", k)
	}
}

func TestUTCOffsetSecondsIsTheZonesOffset(t *testing.T) {
	at := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	if off := calendar.UTCOffsetSeconds(jakarta, at); off != 7*3600 {
		t.Errorf("WIB offset = %d, want %d", off, 7*3600)
	}
}

// Calendar arithmetic has to survive a daylight-saving change, which is why
// DaysBetween compares dates at noon UTC rather than subtracting instants.
func TestDaysBetweenIsCalendarArithmetic(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata for America/New_York")
	}
	// 1 Nov 2026 is the day the clocks go back there: that calendar day is 25
	// hours long, and a naive subtraction would call this one day plus a bit.
	from := time.Date(2026, 10, 31, 0, 0, 0, 0, ny)
	to := time.Date(2026, 11, 1, 0, 0, 0, 0, ny)
	if got := calendar.DaysBetween(from, to); got != 1 {
		t.Errorf("DaysBetween across a DST change = %d, want 1", got)
	}
	if got := calendar.DaysBetween(to, from); got != -1 {
		t.Errorf("DaysBetween backwards = %d, want -1", got)
	}
	same := time.Date(2026, 10, 22, 23, 59, 0, 0, jakarta)
	if got := calendar.DaysBetween(same, same); got != 0 {
		t.Errorf("DaysBetween of one day = %d, want 0", got)
	}
}

func TestStartOfHelpers(t *testing.T) {
	at := time.Date(2026, 10, 22, 15, 42, 7, 123, jakarta) // a Thursday
	if got := calendar.StartOfDay(at); !got.Equal(time.Date(2026, 10, 22, 0, 0, 0, 0, jakarta)) {
		t.Errorf("StartOfDay = %v", got)
	}
	if got := calendar.StartOfMonth(at); !got.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, jakarta)) {
		t.Errorf("StartOfMonth = %v", got)
	}
	if got := calendar.StartOfWeek(at); !got.Equal(time.Date(2026, 10, 19, 0, 0, 0, 0, jakarta)) {
		t.Errorf("StartOfWeek = %v, want Monday 19 Oct", got)
	}
	// A Monday is its own week's start, not the previous week's.
	monday := time.Date(2026, 10, 19, 8, 0, 0, 0, jakarta)
	if got := calendar.StartOfWeek(monday); !got.Equal(time.Date(2026, 10, 19, 0, 0, 0, 0, jakarta)) {
		t.Errorf("StartOfWeek of a Monday = %v", got)
	}
}
