package sqlstore

import "time"

// date-fns 2.30.0 (Kutt's pinned version) ported for UTC. Kutt runs in a UTC container
// (node:22-alpine, no TZ) and stats use the process local time zone, so UTC is the faithful
// choice. Each function mirrors node_modules/date-fns/<name>/index.js line by line.

// jsDate builds a time with JS Date overflow semantics (Go's time.Date normalizes the same way).
func jsDate(y int, m time.Month, d, h, mi, s, ns int) time.Time {
	return time.Date(y, m, d, h, mi, s, ns, time.UTC)
}

// addMonths / subMonths (date-fns addMonths): clamps to the last day of the target month.
func addMonths(t time.Time, amount int) time.Time {
	if amount == 0 {
		return t
	}
	t = t.UTC()
	day := t.Day()
	// endOfDesiredMonth.setMonth(month + amount + 1, 0)
	end := jsDate(t.Year(), t.Month()+time.Month(amount)+1, 0, t.Hour(), t.Minute(), t.Second(), t.Nanosecond())
	if day >= end.Day() {
		return end
	}
	return jsDate(end.Year(), end.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond())
}

func subMonths(t time.Time, n int) time.Time { return addMonths(t, -n) }

func compareAsc(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	}
	return 0
}

func truncDiv(a, b int64) int64 { return a / b } // Go integer division truncates toward zero

// differenceInHours: trunc(ms / 3_600_000).
func differenceInHours(l, r time.Time) int {
	return int(truncDiv(l.Sub(r).Milliseconds(), 3_600_000))
}

func startOfDay(t time.Time) time.Time {
	return jsDate(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0)
}

func differenceInCalendarDays(l, r time.Time) int {
	d := startOfDay(l).Sub(startOfDay(r)).Milliseconds()
	// Math.round(x / 86400000); in UTC d is an exact multiple.
	return int(d / 86_400_000)
}

// differenceInDays (date-fns 2.30 full-day difference using compareLocalAsc).
func differenceInDays(l, r time.Time) int {
	l, r = l.UTC(), r.UTC()
	sign := compareAsc(l, r)
	diff := differenceInCalendarDays(l, r)
	if diff < 0 {
		diff = -diff
	}
	l2 := jsDate(l.Year(), l.Month(), l.Day()-sign*diff, l.Hour(), l.Minute(), l.Second(), l.Nanosecond())
	notFull := 0
	if compareAsc(l2, r) == -sign {
		notFull = 1
	}
	res := sign * (diff - notFull)
	return res
}

func differenceInCalendarMonths(l, r time.Time) int {
	return (l.Year()-r.Year())*12 + int(l.Month()) - int(r.Month())
}

func isLastDayOfMonth(t time.Time) bool {
	return jsDate(t.Year(), t.Month()+1, 0, 0, 0, 0, 0).Day() == t.Day()
}

// differenceInMonths (date-fns 2.30).
func differenceInMonths(left, r time.Time) int {
	left, r = left.UTC(), r.UTC()
	l := left
	sign := compareAsc(l, r)
	diff := differenceInCalendarMonths(l, r)
	if diff < 0 {
		diff = -diff
	}
	if diff < 1 {
		return 0
	}
	if l.Month() == time.February && l.Day() > 27 {
		l = jsDate(l.Year(), l.Month(), 30, l.Hour(), l.Minute(), l.Second(), l.Nanosecond())
	}
	// dateLeft.setMonth(dateLeft.getMonth() - sign * difference) — keeps the day, JS overflow.
	l = jsDate(l.Year(), l.Month()-time.Month(sign*diff), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond())
	notFull := compareAsc(l, r) == -sign
	if isLastDayOfMonth(left) && diff == 1 && compareAsc(left, r) == 1 {
		notFull = false
	}
	n := 0
	if notFull {
		n = 1
	}
	return sign * (diff - n)
}
