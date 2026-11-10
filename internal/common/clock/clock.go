// Package clock is the system's source of time, injected rather than read from
// the global one, so a test can fix "now" and step it forward.
package clock

import "time"

// Clock reports the current time. Every layer takes one instead of calling
// time.Now, which is what lets the tests run the payment window and the session
// idle timeouts without waiting.
type Clock struct{ now func() time.Time }

// New returns a Clock over now, or over time.Now when now is nil.
func New(now func() time.Time) Clock {
	if now == nil {
		now = time.Now
	}
	return Clock{now: now}
}

// Now is the current time in UTC, cut to the millisecond that DATETIME(3)
// stores, so a value written and read back compares equal.
func (c Clock) Now() time.Time { return c.now().UTC().Truncate(time.Millisecond) }
