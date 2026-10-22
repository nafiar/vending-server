package core

import (
	"strconv"
	"time"
)

const dayLayout = "2006-01-02"

// DateRange is a span of whole calendar days in the server's time zone, as the
// admin web sends it (from and to are both included). Start and End are the
// matching UTC instants for SQL: created_at >= Start AND created_at < End.
type DateRange struct {
	From, To   string
	Start, End time.Time
	first      time.Time // local midnight of From
	last       time.Time // local midnight of To
}

// parseRange reads from/to (YYYY-MM-DD). Empty values default to the last
// defaultDays days, ending today.
func (s *Service) parseRange(from, to string, defaultDays, maxDays int) (DateRange, error) {
	fe := fieldErrors{}
	today := startOfDay(s.now().In(s.loc))

	last := today
	if to != "" {
		t, err := time.ParseInLocation(dayLayout, to, s.loc)
		if err != nil {
			fe.add("to", "Tanggal akhir harus berformat YYYY-MM-DD.")
		}
		last = t
	}
	first := last.AddDate(0, 0, -(defaultDays - 1))
	if from != "" {
		t, err := time.ParseInLocation(dayLayout, from, s.loc)
		if err != nil {
			fe.add("from", "Tanggal awal harus berformat YYYY-MM-DD.")
		}
		first = t
	}
	if err := fe.err(); err != nil {
		return DateRange{}, err
	}
	if first.After(last) {
		fe.add("from", "Tanggal awal harus sebelum atau sama dengan tanggal akhir.")
	} else if days := daysBetween(first, last) + 1; days > maxDays {
		fe.add("from", "Rentang tanggal paling panjang "+strconv.Itoa(maxDays)+" hari.")
	}
	if err := fe.err(); err != nil {
		return DateRange{}, err
	}
	end := last.AddDate(0, 0, 1)
	return DateRange{
		From:  first.Format(dayLayout),
		To:    last.Format(dayLayout),
		Start: first.UTC(),
		End:   end.UTC(),
		first: first,
		last:  last,
	}, nil
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func startOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
}

// startOfWeek is the Monday of t's week (ISO weeks, as the admin chart labels them).
func startOfWeek(t time.Time) time.Time {
	d := startOfDay(t)
	offset := (int(d.Weekday()) + 6) % 7 // Monday = 0
	return d.AddDate(0, 0, -offset)
}

func daysBetween(a, b time.Time) int {
	// Calendar arithmetic, safe across DST: compare the dates at noon UTC.
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	ua := time.Date(ay, am, ad, 12, 0, 0, 0, time.UTC)
	ub := time.Date(by, bm, bd, 12, 0, 0, 0, time.UTC)
	return int(ub.Sub(ua).Hours() / 24)
}

// period is one bar of the recap chart.
type period struct {
	key   string
	start time.Time
}

// periodKey is the bucket a local day falls in.
func periodKey(day time.Time, groupBy string) string {
	switch groupBy {
	case "week":
		return startOfWeek(day).Format(dayLayout)
	case "month":
		return day.Format("2006-01")
	default:
		return day.Format(dayLayout)
	}
}

// periods lists every bucket that overlaps the range, empty ones included,
// so the chart shows days without sales as zero instead of skipping them.
func periods(r DateRange, groupBy string) []period {
	var out []period
	var t time.Time
	switch groupBy {
	case "week":
		t = startOfWeek(r.first)
	case "month":
		t = startOfMonth(r.first)
	default:
		t = r.first
	}
	for !t.After(r.last) {
		out = append(out, period{key: periodKey(t, groupBy), start: t})
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

// utcOffsetSeconds is the zone's offset from UTC at t. Recap queries shift
// created_at by it to group sales by local calendar day inside MySQL, which is
// exact for zones without daylight saving time, such as WIB (UTC+7).
func utcOffsetSeconds(loc *time.Location, t time.Time) int {
	_, off := t.In(loc).Zone()
	return off
}
