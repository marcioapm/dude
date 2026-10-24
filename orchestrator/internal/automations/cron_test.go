package automations

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func nextN(t *testing.T, expr string, from time.Time, loc *time.Location, n int) []string {
	t.Helper()
	c, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	var out []string
	for i := 0; i < n; i++ {
		from = c.Next(from, loc)
		if from.IsZero() {
			out = append(out, "never")
			break
		}
		out = append(out, from.Format("2006-01-02 15:04 MST"))
	}
	return out
}

func eq(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", name, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
	}
}

// The schedule picker's presets: Minutes, Hourly, Daily, Weekly, and a
// cron of one's own, each read in Lisbon's zone.
func TestCronPresets(t *testing.T) {
	lisbon := mustLoc(t, "Europe/Lisbon")
	from := time.Date(2026, 10, 14, 10, 7, 30, 0, lisbon) // a Wednesday
	for _, c := range []struct {
		name, expr string
		want       []string
	}{
		{"every 15 minutes", "*/15 * * * *", []string{"2026-10-14 10:15 WEST", "2026-10-14 10:30 WEST", "2026-10-14 10:45 WEST"}},
		{"hourly at :05", "5 * * * *", []string{"2026-10-14 11:05 WEST", "2026-10-14 12:05 WEST", "2026-10-14 13:05 WEST"}},
		{"daily at 03:00", "0 3 * * *", []string{"2026-10-15 03:00 WEST", "2026-10-16 03:00 WEST", "2026-10-17 03:00 WEST"}},
		{"weekdays at 09:30", "30 9 * * 1-5", []string{"2026-10-15 09:30 WEST", "2026-10-16 09:30 WEST", "2026-10-19 09:30 WEST"}},
		{"weekly, Monday and Thursday 08:00", "0 8 * * 1,4", []string{"2026-10-15 08:00 WEST", "2026-10-19 08:00 WEST", "2026-10-22 08:00 WEST"}},
		{"Sunday as 7", "0 12 * * 7", []string{"2026-10-18 12:00 WEST", "2026-10-25 12:00 WET", "2026-11-01 12:00 WET"}},
		{"first of the month", "0 0 1 * *", []string{"2026-11-01 00:00 WET", "2026-12-01 00:00 WET", "2027-01-01 00:00 WET"}},
		{"day of month or weekday", "0 6 13 * 5", []string{"2026-10-16 06:00 WEST", "2026-10-23 06:00 WEST", "2026-10-30 06:00 WET"}},
		{"never", "0 0 31 2 *", []string{"never"}},
	} {
		eq(t, c.name, nextN(t, c.expr, from, lisbon, len(c.want)), c.want)
	}
}

// Spring forward in New York (2026-03-08, 02:00 → 03:00): 02:30 does not
// exist that day and is skipped; the hour after is there.
func TestCronSpringForwardSkipsTheMissingMinute(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	from := time.Date(2026, 3, 7, 12, 0, 0, 0, ny)
	eq(t, "02:30 daily", nextN(t, "30 2 * * *", from, ny, 3),
		[]string{"2026-03-09 02:30 EDT", "2026-03-10 02:30 EDT", "2026-03-11 02:30 EDT"})
	eq(t, "every 30 min across the gap", nextN(t, "*/30 * * * *", time.Date(2026, 3, 8, 1, 15, 0, 0, ny), ny, 3),
		[]string{"2026-03-08 01:30 EST", "2026-03-08 03:00 EDT", "2026-03-08 03:30 EDT"})
}

// Fall back in New York (2026-11-01, 02:00 → 01:00): 01:30 shows twice on
// the wall and fires once, at the first.
func TestCronFallBackFiresTheRepeatedMinuteOnce(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	from := time.Date(2026, 10, 31, 12, 0, 0, 0, ny)
	eq(t, "01:30 daily", nextN(t, "30 1 * * *", from, ny, 3),
		[]string{"2026-11-01 01:30 EDT", "2026-11-02 01:30 EST", "2026-11-03 01:30 EST"})
	eq(t, "hourly across the repeat", nextN(t, "0 * * * *", time.Date(2026, 11, 1, 0, 30, 0, 0, ny), ny, 3),
		[]string{"2026-11-01 01:00 EDT", "2026-11-01 02:00 EST", "2026-11-01 03:00 EST"})
}

// A zone a half-hour off (Lord Howe falls back 30 minutes).
func TestCronHalfHourFallBack(t *testing.T) {
	lh := mustLoc(t, "Australia/Lord_Howe")
	from := time.Date(2026, 4, 4, 12, 0, 0, 0, lh)
	got := nextN(t, "45 1 * * *", from, lh, 2)
	if len(got) != 2 || got[0][:16] != "2026-04-05 01:45" || got[1][:16] != "2026-04-06 01:45" {
		t.Fatalf("got %v", got)
	}
}

func TestCronRefusals(t *testing.T) {
	for expr, want := range map[string]string{
		"0 3 * *":     "a schedule is five fields: minute hour day-of-month month day-of-week",
		"60 * * * *":  `minute: "60" is outside 0–59`,
		"0 24 * * *":  `hour: "24" is outside 0–23`,
		"0 0 0 * *":   `day of month: "0" is outside 1–31`,
		"0 0 * 13 *":  `month: "13" is outside 1–12`,
		"0 0 * * 8":   `day of week: "8" is outside 0–7`,
		"*/0 * * * *": `minute: "*/0" has no step of 1 or more`,
		"a * * * *":   `minute: "a" is not a value, a range or a step`,
		"5-1 * * * *": `minute: "5-1" is outside 0–59`,
	} {
		if _, err := ParseCron(expr); err == nil || err.Error() != want {
			t.Errorf("%q: got %v, want %q", expr, err, want)
		}
	}
}
