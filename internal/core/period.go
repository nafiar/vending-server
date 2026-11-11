package core

import (
	"time"

	"vending/internal/common/calendar"
)

// The calendar arithmetic now lives in common/calendar, which the usecases and
// the remaining core stories share. These wrappers keep core's call sites
// unchanged while the last of it is pulled apart; they go with core itself
// (docs/REARCHITECTURE.md phase 8).

const dayLayout = calendar.DayLayout

type DateRange = calendar.DateRange

func (s *Service) parseRange(from, to string, defaultDays, maxDays int) (DateRange, error) {
	return calendar.ParseRange(s.now(), s.loc, from, to, defaultDays, maxDays)
}

func startOfDay(t time.Time) time.Time   { return calendar.StartOfDay(t) }
func startOfMonth(t time.Time) time.Time { return calendar.StartOfMonth(t) }

func periodKey(day time.Time, groupBy string) string { return calendar.PeriodKey(day, groupBy) }

func periods(r DateRange, groupBy string) []calendar.Period { return calendar.Periods(r, groupBy) }

func utcOffsetSeconds(loc *time.Location, t time.Time) int {
	return calendar.UTCOffsetSeconds(loc, t)
}
