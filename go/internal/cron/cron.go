// Package cron parses the subset of cron expressions rex accepts for
// recurring callbacks: five fields (min hour day mon weekday), each
// either "*" or a literal integer. The weekday field additionally
// supports "N-M" ranges.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Schedule struct {
	Minute  *int
	Hour    *int
	Day     *int
	Month   *int
	Weekday []int // empty => any; multiple values => range expanded
}

func Parse(expr string) (*Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d", len(fields))
	}
	s := &Schedule{}
	parsers := []struct {
		name   string
		target **int
		val    string
	}{
		{"minute", &s.Minute, fields[0]},
		{"hour", &s.Hour, fields[1]},
		{"day", &s.Day, fields[2]},
		{"month", &s.Month, fields[3]},
	}
	for _, p := range parsers {
		v, err := parseStar(p.val)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.name, err)
		}
		*p.target = v
	}

	wd := fields[4]
	switch {
	case wd == "*":
		// leave s.Weekday empty
	case strings.Contains(wd, "-"):
		a, b, ok := strings.Cut(wd, "-")
		if !ok {
			return nil, fmt.Errorf("invalid weekday range: %s", wd)
		}
		start, err := strconv.Atoi(a)
		if err != nil {
			return nil, fmt.Errorf("invalid weekday range: %s", wd)
		}
		end, err := strconv.Atoi(b)
		if err != nil {
			return nil, fmt.Errorf("invalid weekday range: %s", wd)
		}
		for i := start; i <= end; i++ {
			s.Weekday = append(s.Weekday, i)
		}
	default:
		n, err := strconv.Atoi(wd)
		if err != nil {
			return nil, fmt.Errorf("invalid weekday: %s", wd)
		}
		s.Weekday = []int{n}
	}
	return s, nil
}

func parseStar(v string) (*int, error) {
	if v == "*" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("invalid value %q", v)
	}
	return &n, nil
}

// Matches reports whether the schedule fires at t (minute resolution).
func (s *Schedule) Matches(t time.Time) bool {
	if s.Minute != nil && t.Minute() != *s.Minute {
		return false
	}
	if s.Hour != nil && t.Hour() != *s.Hour {
		return false
	}
	if s.Day != nil && t.Day() != *s.Day {
		return false
	}
	if s.Month != nil && int(t.Month()) != *s.Month {
		return false
	}
	if len(s.Weekday) > 0 {
		// cron weekdays are 0=Sunday..6=Saturday, which matches time.Weekday.
		wd := int(t.Weekday())
		ok := false
		for _, w := range s.Weekday {
			if w == wd {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
