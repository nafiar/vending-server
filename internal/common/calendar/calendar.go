// Package calendar is the calendar arithmetic the reports are built on: whole
// local days, the weeks and months they group into, and the UTC instants that
// bound them for a query.
//
// It lives in common rather than next to one report because almost every read
// in the system needs it: the recap chart, the machine cards' month revenue,
// an operator's visits over 30 days, the change log and the two histories.
// docs/REARCHITECTURE.md expected a single consumer; there are six.
//
// Everything here is pure. The caller passes the current time and the zone, so
// a test fixes both and no function reads a clock of its own.
//
// One idea runs through all of it: a day is a thing that happens in a place.
// "Today" for this system is a day in Asia/Jakarta, and the database stores
// UTC, so every range is built in the local zone and converted once, at the
// edge, rather than reasoned about in UTC and hoped over.
package calendar

import (
	"strconv"
	"time"

	"vending/internal/model"
)

// DayLayout is the date format the admin web sends and the chart labels use.
const DayLayout = "2006-01-02"

// DateRange is a span of whole calendar days in the server's time zone, with
// both ends included, as the admin web asks for it.
//
// Start and End are the matching UTC instants for a query, half-open:
// created_at >= Start AND created_at < End. Half-open is what makes the last
// day whole without anyone writing 23:59:59.999.
type DateRange struct {
	From, To   string
	Start, End time.Time

	first time.Time // local midnight of From
	last  time.Time // local midnight of To
}

// ParseRange reads a from/to pair in YYYY-MM-DD, in the zone given. Empty
// values mean the last defaultDays days ending today, and a span longer than
// maxDays is refused.
//
// Both dates are reported on if both are wrong, so a form marks both fields.
func ParseRange(now time.Time, loc *time.Location, from, to string, defaultDays, maxDays int) (DateRange, error) {
	fe := model.FieldErrors{}
	today := StartOfDay(now.In(loc))

	last := today
	if to != "" {
		t, err := time.ParseInLocation(DayLayout, to, loc)
		if err != nil {
			fe.Add("to", "Tanggal akhir harus berformat YYYY-MM-DD.")
		}
		last = t
	}
	first := last.AddDate(0, 0, -(defaultDays - 1))
	if from != "" {
		t, err := time.ParseInLocation(DayLayout, from, loc)
		if err != nil {
			fe.Add("from", "Tanggal awal harus berformat YYYY-MM-DD.")
		}
		first = t
	}
	// The format is checked before the span, or an unparseable date would be
	// reported as a range two thousand years long.
	if err := fe.Err(); err != nil {
		return DateRange{}, err
	}

	if first.After(last) {
		fe.Add("from", "Tanggal awal harus sebelum atau sama dengan tanggal akhir.")
	} else if days := DaysBetween(first, last) + 1; days > maxDays {
		fe.Add("from", "Rentang tanggal paling panjang "+strconv.Itoa(maxDays)+" hari.")
	}
	if err := fe.Err(); err != nil {
		return DateRange{}, err
	}

	return DateRange{
		From:  first.Format(DayLayout),
		To:    last.Format(DayLayout),
		Start: first.UTC(),
		End:   last.AddDate(0, 0, 1).UTC(),
		first: first,
		last:  last,
	}, nil
}

// StartOfDay is local midnight of t's day.
func StartOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// StartOfMonth is local midnight of the first of t's month.
func StartOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
}

// StartOfWeek is the Monday of t's week: ISO weeks, as the chart labels them.
func StartOfWeek(t time.Time) time.Time {
	d := StartOfDay(t)
	offset := (int(d.Weekday()) + 6) % 7 // Monday = 0
	return d.AddDate(0, 0, -offset)
}

// DaysBetween counts whole calendar days from a to b.
func DaysBetween(a, b time.Time) int {
	// Calendar arithmetic, safe across a daylight-saving change: compare the
	// dates at noon UTC, where no shift can push a day over a boundary.
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	ua := time.Date(ay, am, ad, 12, 0, 0, 0, time.UTC)
	ub := time.Date(by, bm, bd, 12, 0, 0, 0, time.UTC)
	return int(ub.Sub(ua).Hours() / 24)
}

// Period is one bucket of a report: one bar of the chart.
type Period struct {
	Key   string
	Start time.Time
}

// PeriodKey is the bucket a local day falls in: the day itself, the Monday of
// its week, or its month.
func PeriodKey(day time.Time, groupBy string) string {
	switch groupBy {
	case "week":
		return StartOfWeek(day).Format(DayLayout)
	case "month":
		return day.Format("2006-01")
	default:
		return day.Format(DayLayout)
	}
}

// Periods lists every bucket that overlaps the range, including the empty ones,
// so a chart shows a day without sales as zero rather than leaving it out and
// making a quiet week look like a short one.
func Periods(r DateRange, groupBy string) []Period {
	var out []Period
	var t time.Time
	switch groupBy {
	case "week":
		t = StartOfWeek(r.first)
	case "month":
		t = StartOfMonth(r.first)
	default:
		t = r.first
	}
	for !t.After(r.last) {
		out = append(out, Period{Key: PeriodKey(t, groupBy), Start: t})
		switch groupBy {
		case "week":
			t = t.AddDate(0, 0, 7)
		case "month":
			t = t.AddDate(0, 1, 0)
		default:
			t = t.AddDate(0, 0, 1)
		}
	}
	return out
}

// UTCOffsetSeconds is the zone's offset from UTC at t.
//
// The recap queries shift created_at by it to group sales by local calendar day
// inside MySQL. That is exact for a zone without daylight saving time, which
// Asia/Jakarta is; for a zone that has it, a range spanning the change would be
// off by an hour at the seam.
func UTCOffsetSeconds(loc *time.Location, t time.Time) int {
	_, off := t.In(loc).Zone()
	return off
}
