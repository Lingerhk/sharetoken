package warmup

import (
	"time"

	"sharetoken/internal/provider"
)

// warmSlack is how far into a window it still counts as not started: the
// ChatGPT backend reports a window nobody started as resetting a whole
// window from now.
const warmSlack = 10 * time.Minute

// asOf is w as of now: a window whose reset has passed has started over,
// from nothing, and is not running.
func asOf(w provider.UsageWindow, now time.Time) provider.UsageWindow {
	if !w.ResetsAt.IsZero() && !now.Before(w.ResetsAt) {
		w.Used, w.ResetsAt = 0, time.Time{}
	}
	return w
}

// idle says whether w (as of now) hasn't started: nothing used, and its
// reset unknown or a whole window away.
func idle(w provider.UsageWindow, now time.Time) bool {
	w = asOf(w, now)
	return w.Used == 0 && (w.ResetsAt.IsZero() || w.ResetsAt.Sub(now) >= w.Span()-warmSlack)
}

// short says whether w is the 5-hour kind of window, not the weekly one.
func short(w provider.UsageWindow) bool { return w.Span() > 0 && w.Span() < 24*time.Hour }

// usedUp says whether one of l's windows that still runs at now is spent,
// or the vendor turned the account away or bills it past its plan: a
// request now would go on extra usage or credits, or fail.
func usedUp(l *provider.UsageLimits, now time.Time) bool {
	if l == nil {
		return false
	}
	if l.Overage {
		return true
	}
	for _, w := range l.Windows {
		if w2 := asOf(w, now); w2.Used >= 100 {
			return true
		}
	}
	// a refusal stands until the windows it was about could have reset
	return l.Rejected && now.Sub(l.At) < time.Hour
}

// window is l's window of the kind wanted (short: the 5-hour one).
func window(l *provider.UsageLimits, wantShort bool) (provider.UsageWindow, bool) {
	if l == nil {
		return provider.UsageWindow{}, false
	}
	for _, w := range l.Windows {
		if w.Span() > 0 && short(w) == wantShort {
			return w, true
		}
	}
	return provider.UsageWindow{}, false
}

// known says whether l tells any window.
func known(l *provider.UsageLimits) bool { return l != nil && len(l.Windows) > 0 }
