// Package cron provides a minimal cron expression parser and scheduler.
// Supports standard 5-field cron expressions: minute hour day-of-month month day-of-week.
//
// Field syntax:
//   - *        any value
//   - N        exact value
//   - N,M      list
//   - N-M      range
//   - */N      step (every N)
//   - N-M/S    range with step
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule represents a parsed cron expression.
type Schedule struct {
	Minutes    []bool // [0..59]
	Hours      []bool // [0..23]
	DaysOfMonth []bool // [1..31]
	Months     []bool // [1..12]
	DaysOfWeek []bool // [0..6] (0=Sunday)
	Raw        string
}

// Parse parses a standard 5-field cron expression.
func Parse(expr string) (*Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields, got %d in %q", len(fields), expr)
	}

	minutes, err := parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("cron: minute field: %w", err)
	}
	hours, err := parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("cron: hour field: %w", err)
	}
	dom, err := parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("cron: day-of-month field: %w", err)
	}
	months, err := parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("cron: month field: %w", err)
	}
	dow, err := parseField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("cron: day-of-week field: %w", err)
	}

	return &Schedule{
		Minutes:     minutes,
		Hours:       hours,
		DaysOfMonth: dom,
		Months:      months,
		DaysOfWeek:  dow,
		Raw:         expr,
	}, nil
}

// Matches returns true if the given time matches this schedule.
func (s *Schedule) Matches(t time.Time) bool {
	return s.Minutes[t.Minute()] &&
		s.Hours[t.Hour()] &&
		s.DaysOfMonth[t.Day()] &&
		s.Months[int(t.Month())] &&
		s.DaysOfWeek[int(t.Weekday())]
}

// NextAfter returns the next time after 'after' that matches the schedule.
// Searches up to 366 days ahead. Returns zero time if no match found.
func (s *Schedule) NextAfter(after time.Time) time.Time {
	// Start from the next minute boundary
	t := after.Truncate(time.Minute).Add(time.Minute)

	// Search up to ~366 days (527040 minutes)
	for i := 0; i < 527040; i++ {
		if s.Matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// parseField parses a single cron field into a boolean slice.
// The slice is indexed from 0 to max (inclusive), but values below min are unused.
func parseField(field string, min, max int) ([]bool, error) {
	result := make([]bool, max+1)

	// Handle comma-separated list
	parts := strings.Split(field, ",")
	for _, part := range parts {
		if err := parsePart(part, min, max, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func parsePart(part string, min, max int, result []bool) error {
	// Check for step: */N or N-M/N
	step := 1
	if idx := strings.Index(part, "/"); idx >= 0 {
		var err error
		step, err = strconv.Atoi(part[idx+1:])
		if err != nil || step <= 0 {
			return fmt.Errorf("invalid step in %q", part)
		}
		part = part[:idx]
	}

	// Wildcard
	if part == "*" {
		for i := min; i <= max; i += step {
			result[i] = true
		}
		return nil
	}

	// Range: N-M
	if idx := strings.Index(part, "-"); idx >= 0 {
		lo, err := strconv.Atoi(part[:idx])
		if err != nil {
			return fmt.Errorf("invalid range start in %q", part)
		}
		hi, err := strconv.Atoi(part[idx+1:])
		if err != nil {
			return fmt.Errorf("invalid range end in %q", part)
		}
		if lo < min || hi > max || lo > hi {
			return fmt.Errorf("range %d-%d out of bounds [%d,%d]", lo, hi, min, max)
		}
		for i := lo; i <= hi; i += step {
			result[i] = true
		}
		return nil
	}

	// Single value
	val, err := strconv.Atoi(part)
	if err != nil {
		return fmt.Errorf("invalid value %q", part)
	}
	if val < min || val > max {
		return fmt.Errorf("value %d out of bounds [%d,%d]", val, min, max)
	}
	if step == 1 {
		result[val] = true
	} else {
		for i := val; i <= max; i += step {
			result[i] = true
		}
	}
	return nil
}
