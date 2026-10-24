package automations

// Schedules: five-field cron in a time zone. Each field is a set; the next
// firing is the first minute after a time whose wall clock in the zone is
// in every set. Wall-clock minutes a DST jump skips never fire; a minute a
// fall-back repeats fires once, at its first occurrence.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field schedule.
type Cron struct {
	minutes, hours, days, months, weekdays [64]bool
	// Both day fields restricted: cron fires on either.
	dayOr bool
	expr  string
}

var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
var cronNames = [5]string{"minute", "hour", "day of month", "month", "day of week"}

// ParseCron reads `minute hour day-of-month month day-of-week`: each a
// list of `*`, `n`, `a-b`, with an optional `/step`. 7 is Sunday, as 0.
func ParseCron(expr string) (Cron, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Cron{}, fmt.Errorf("a schedule is five fields: minute hour day-of-month month day-of-week")
	}
	c := Cron{expr: expr, dayOr: fields[2] != "*" && fields[4] != "*"}
	sets := []*[64]bool{&c.minutes, &c.hours, &c.days, &c.months, &c.weekdays}
	for i, f := range fields {
		if err := parseField(f, cronBounds[i][0], cronBounds[i][1], sets[i]); err != nil {
			return Cron{}, fmt.Errorf("%s: %w", cronNames[i], err)
		}
	}
	if c.weekdays[7] {
		c.weekdays[0] = true
	}
	return c, nil
}

func parseField(f string, lo, hi int, set *[64]bool) error {
	for _, part := range strings.Split(f, ",") {
		rng, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return fmt.Errorf("%q has no step of 1 or more", part)
			}
			step = n
		}
		from, to := lo, hi
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			x, err := strconv.Atoi(a)
			if err != nil {
				return fmt.Errorf("%q is not a value, a range or a step", part)
			}
			from, to = x, x
			if isRange {
				if to, err = strconv.Atoi(b); err != nil {
					return fmt.Errorf("%q is not a value, a range or a step", part)
				}
			} else if hasStep {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return fmt.Errorf("%q is outside %d–%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return nil
}

func (c Cron) String() string { return c.expr }

func (c Cron) dayMatches(t time.Time) bool {
	dom, dow := c.days[t.Day()], c.weekdays[int(t.Weekday())]
	if c.dayOr {
		return dom || dow
	}
	return dom && dow
}

// Next is the first firing strictly after `after`, in loc. It walks real
// time, reading each instant's wall clock in loc and jumping over months,
// days and hours that cannot match. So a wall minute a spring-forward
// skips never fires, and one a fall-back repeats fires at its first
// occurrence only. Searches up to five years; zero if none (a 31 February).
func (c Cron) Next(after time.Time, loc *time.Location) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		w := t.In(loc)
		switch {
		case !c.months[int(w.Month())]:
			t = later(t, time.Date(w.Year(), w.Month()+1, 1, 0, 0, 0, 0, loc))
		case !c.dayMatches(w):
			t = later(t, time.Date(w.Year(), w.Month(), w.Day()+1, 0, 0, 0, 0, loc))
		case !c.hours[w.Hour()]:
			t = t.Add(time.Duration(60-w.Minute()) * time.Minute)
		case !c.minutes[w.Minute()] || repeated(w):
			t = t.Add(time.Minute)
		default:
			return w
		}
	}
	return time.Time{}
}

// later is next, or a minute after t when the zone's normalisation of a
// wall time landed at or before it.
func later(t, next time.Time) time.Time {
	if next.After(t) {
		return next
	}
	return t.Add(time.Minute)
}

// repeated says w's wall clock was already shown earlier: w is in the
// second pass of a fall-back (by 30 minutes or an hour).
func repeated(w time.Time) bool {
	for _, d := range []time.Duration{30 * time.Minute, time.Hour} {
		e := w.Add(-d).In(w.Location())
		if e.Hour() == w.Hour() && e.Minute() == w.Minute() && e.Day() == w.Day() {
			return true
		}
	}
	return false
}
