package warmup

import (
	"time"

	"sharetoken/internal/provider"
)

// Times of day, as magpie's daily warm-up keeps them: a 5-hour window
// starts at an account's first request, so a day that begins at 9 fits two
// windows before the evening, and one started at 6, while the user sleeps,
// runs out at 11 and the day fits three.

// dailySlack is how late a time of day is still kept: a machine that slept
// through it and wakes within this sends it then; later, a window started
// hours late is not the one wanted.
const dailySlack = time.Hour

// dayStart is the latest of the times of day ats at or before now, in now's
// zone; false when ats has none.
func dayStart(ats []string, now time.Time) (time.Time, bool) {
	var latest time.Time
	found := false
	for _, at := range ats {
		h, m, ok := provider.ParseClock(at)
		if !ok {
			continue
		}
		y, mo, d := now.Date()
		t := time.Date(y, mo, d, h, m, 0, 0, now.Location())
		if t.After(now) {
			t = time.Date(y, mo, d-1, h, m, 0, 0, now.Location())
		}
		if !found || t.After(latest) {
			latest, found = t, true
		}
	}
	return latest, found
}

// nextDayStart is the first of the times of day ats after now; false when
// ats has none.
func nextDayStart(ats []string, now time.Time) (time.Time, bool) {
	var first time.Time
	found := false
	for _, at := range ats {
		h, m, ok := provider.ParseClock(at)
		if !ok {
			continue
		}
		y, mo, d := now.Date()
		t := time.Date(y, mo, d, h, m, 0, 0, now.Location())
		if !t.After(now) {
			t = time.Date(y, mo, d+1, h, m, 0, 0, now.Location())
		}
		if !found || t.Before(first) {
			first, found = t, true
		}
	}
	return first, found
}

// slotKey names a time of day on a day, as the state keeps the last one
// handled: "2006-01-02 15:04".
func slotKey(t time.Time) string { return t.Format("2006-01-02 15:04") }

// dueSlot is the time of day that wants handling now: the latest one, if
// it passed no more than dailySlack ago.
func dueSlot(ats []string, now time.Time) (time.Time, bool) {
	t, ok := dayStart(ats, now)
	if !ok || now.Sub(t) > dailySlack {
		return time.Time{}, false
	}
	return t, true
}

// heldForDay says whether a window of span started now would still run at
// the next time of day: started on its reset, it would push that time's
// window later, so it waits for the time instead.
func heldForDay(ats []string, span time.Duration, now time.Time) bool {
	next, ok := nextDayStart(ats, now)
	return ok && next.Sub(now) < span
}
