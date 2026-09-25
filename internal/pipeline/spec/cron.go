package spec

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed standard 5-field cron expression (minute hour day-of-month month
// day-of-week), evaluated in UTC. Supported: "*", lists "1,5", ranges "1-5", steps "*/15" and
// "10-50/10", month and weekday names (JAN, MON), 7 as Sunday, and the macros @hourly,
// @daily (@midnight), @weekly, @monthly, @yearly (@annually).
type Cron struct {
	minute, hour, dom, month, dow uint64 // bit sets
	domAny, dowAny                bool   // field was "*": standard cron OR-semantics otherwise
}

var cronMacros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var (
	monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
	dayNames   = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}
)

// ParseCron parses a cron expression.
func ParseCron(expr string) (Cron, error) {
	expr = strings.TrimSpace(expr)
	if m, ok := cronMacros[strings.ToLower(expr)]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Cron{}, fmt.Errorf("expected 5 fields (minute hour day month weekday), got %d", len(f))
	}
	var c Cron
	var err error
	if c.minute, err = parseField(f[0], 0, 59, nil); err != nil {
		return Cron{}, fmt.Errorf("minute: %w", err)
	}
	if c.hour, err = parseField(f[1], 0, 23, nil); err != nil {
		return Cron{}, fmt.Errorf("hour: %w", err)
	}
	if c.dom, err = parseField(f[2], 1, 31, nil); err != nil {
		return Cron{}, fmt.Errorf("day of month: %w", err)
	}
	if c.month, err = parseField(f[3], 1, 12, monthNames); err != nil {
		return Cron{}, fmt.Errorf("month: %w", err)
	}
	if c.dow, err = parseField(f[4], 0, 7, dayNames); err != nil {
		return Cron{}, fmt.Errorf("day of week: %w", err)
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday too
		c.dow |= 1
		c.dow &^= 1 << 7
	}
	c.domAny, c.dowAny = f[2] == "*", f[4] == "*"
	return c, nil
}

func parseField(s string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			return 0, errors.New("empty list item")
		}
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("invalid step %q", stepStr)
			}
			step = n
		}
		start, end := lo, hi
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if start, err = value(a, names); err != nil {
				return 0, err
			}
			end = start
			if isRange {
				if end, err = value(b, names); err != nil {
					return 0, err
				}
			} else if hasStep {
				end = hi // "5/10" means 5, 15, 25, …
			}
		}
		if start < lo || end > hi || start > end {
			return 0, fmt.Errorf("%q is out of range %d-%d", part, lo, hi)
		}
		for v := start; v <= end; v += step {
			bits |= 1 << uint(v) // #nosec G115 -- v is within 0..59
		}
	}
	return bits, nil
}

func value(s string, names map[string]int) (int, error) {
	if n, ok := names[strings.ToUpper(s)]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	return n, nil
}

func has(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 } // #nosec G115 -- v is within 0..59

func (c Cron) dayMatches(t time.Time) bool {
	domOK, dowOK := has(c.dom, t.Day()), has(c.dow, int(t.Weekday()))
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dowOK
	case c.dowAny:
		return domOK
	default:
		return domOK || dowOK // classic cron: either restricted field matches
	}
}

// Next returns the first matching minute strictly after t (UTC), or the zero time when none
// exists within five years (e.g. "0 0 31 2 *").
func (c Cron) Next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case !has(c.month, int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case !has(c.hour, t.Hour()):
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case !has(c.minute, t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
