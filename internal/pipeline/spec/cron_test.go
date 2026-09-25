package spec

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCronNext(t *testing.T) {
	cases := []struct {
		expr, from, want string
	}{
		{"*/15 * * * *", "2026-09-25 10:07", "2026-09-25 10:15"},
		{"*/15 * * * *", "2026-09-25 10:15", "2026-09-25 10:30"}, // strictly after
		{"0 3 * * *", "2026-09-25 10:00", "2026-09-26 03:00"},
		{"@daily", "2026-12-31 23:59", "2027-01-01 00:00"},
		{"@hourly", "2026-09-25 10:00", "2026-09-25 11:00"},
		{"30 9 * * MON-FRI", "2026-09-25 10:00", "2026-09-28 09:30"}, // Fri 25th → Mon 28th
		{"0 0 * * 7", "2026-09-25 10:00", "2026-09-27 00:00"},        // 7 = Sunday
		{"0 0 1 */3 *", "2026-09-25 10:00", "2026-10-01 00:00"},
		{"0 12 29 FEB *", "2026-03-01 00:00", "2028-02-29 12:00"},    // leap day
		{"0 0 13 * FRI", "2026-09-25 10:00", "2026-10-02 00:00"},     // day-of-month OR weekday
		{"10-50/20 8 * * *", "2026-09-25 08:15", "2026-09-25 08:30"}, // 10, 30, 50
		{"5/20 * * * *", "2026-09-25 08:26", "2026-09-25 08:45"},     // 5, 25, 45
		{"0 0 1 jan *", "2026-09-25 10:00", "2027-01-01 00:00"},      // names are case-insensitive
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		require.NoError(t, err, c.expr)
		assert.Equal(t, at(c.want), cr.Next(at(c.from)), "%s from %s", c.expr, c.from)
	}
	never, err := ParseCron("0 0 31 2 *")
	require.NoError(t, err)
	assert.True(t, never.Next(at("2026-01-01 00:00")).IsZero(), "Feb 31st never happens")
}

func TestCronParseErrors(t *testing.T) {
	for _, bad := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *",
		"* * * 13 *", "* * * * 8", "5-1 * * * *", "*/0 * * * *", "a * * * *", "1,,2 * * * *", "@every 5m"} {
		_, err := ParseCron(bad)
		assert.Error(t, err, bad)
	}
}
