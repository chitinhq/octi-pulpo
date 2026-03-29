package cron

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		expr    string
		wantErr bool
	}{
		// Expressions from schedule.json
		{"0 6,18 * * *", false},
		{"0 7,19 * * *", false},
		{"30 */4 * * *", false},
		{"10 */3 * * *", false},
		{"30 1,3,5,7,9,11,13,15,17,19,21,23 * * *", false},
		{"50 */3 * * *", false},
		{"5 * * * *", false},
		{"20 */2 * * *", false},
		{"10 1 * * *", false},
		{"15 1 * * *", false},
		{"25 */2 * * *", false},
		{"40 1 * * *", false},
		{"0 23 * * 0", false},
		{"0 23 * * 6", false},
		{"0 9 * * 6", false},
		{"0 */6 * * *", false},
		{"0 2,10,18 * * *", false},
		// Invalid
		{"", true},
		{"* *", true},
		{"60 * * * *", true},
		{"* 25 * * *", true},
	}

	for _, tt := range tests {
		s, err := Parse(tt.expr)
		if tt.wantErr {
			if err == nil {
				t.Errorf("Parse(%q) expected error", tt.expr)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", tt.expr, err)
			continue
		}
		if s == nil {
			t.Errorf("Parse(%q) returned nil schedule", tt.expr)
		}
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		expr string
		time time.Time
		want bool
	}{
		// "0 6,18 * * *" matches at 06:00 and 18:00
		{"0 6,18 * * *", time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC), true},
		{"0 6,18 * * *", time.Date(2026, 3, 29, 18, 0, 0, 0, time.UTC), true},
		{"0 6,18 * * *", time.Date(2026, 3, 29, 7, 0, 0, 0, time.UTC), false},
		{"0 6,18 * * *", time.Date(2026, 3, 29, 6, 1, 0, 0, time.UTC), false},

		// "5 * * * *" matches at minute 5 of every hour
		{"5 * * * *", time.Date(2026, 3, 29, 0, 5, 0, 0, time.UTC), true},
		{"5 * * * *", time.Date(2026, 3, 29, 14, 5, 0, 0, time.UTC), true},
		{"5 * * * *", time.Date(2026, 3, 29, 14, 6, 0, 0, time.UTC), false},

		// "30 */4 * * *" matches at XX:30 for hours 0,4,8,12,16,20
		{"30 */4 * * *", time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC), true},
		{"30 */4 * * *", time.Date(2026, 3, 29, 4, 30, 0, 0, time.UTC), true},
		{"30 */4 * * *", time.Date(2026, 3, 29, 8, 30, 0, 0, time.UTC), true},
		{"30 */4 * * *", time.Date(2026, 3, 29, 3, 30, 0, 0, time.UTC), false},

		// "0 23 * * 0" matches Sunday at 23:00 (2026-03-29 is a Sunday)
		{"0 23 * * 0", time.Date(2026, 3, 29, 23, 0, 0, 0, time.UTC), true},
		// Monday
		{"0 23 * * 0", time.Date(2026, 3, 30, 23, 0, 0, 0, time.UTC), false},
	}

	for _, tt := range tests {
		s, err := Parse(tt.expr)
		if err != nil {
			t.Fatalf("Parse(%q) failed: %v", tt.expr, err)
		}
		got := s.Matches(tt.time)
		if got != tt.want {
			t.Errorf("Schedule(%q).Matches(%s) = %v, want %v",
				tt.expr, tt.time.Format("2006-01-02 15:04"), got, tt.want)
		}
	}
}

func TestNextAfter(t *testing.T) {
	s, err := Parse("0 6,18 * * *")
	if err != nil {
		t.Fatal(err)
	}

	// After 05:30 on March 29 -> next should be 06:00
	after := time.Date(2026, 3, 29, 5, 30, 0, 0, time.UTC)
	next := s.NextAfter(after)
	expected := time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("NextAfter(%s) = %s, want %s", after.Format("15:04"), next.Format("2006-01-02 15:04"), expected.Format("2006-01-02 15:04"))
	}

	// After 06:00 -> next should be 18:00
	after = time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC)
	next = s.NextAfter(after)
	expected = time.Date(2026, 3, 29, 18, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("NextAfter(%s) = %s, want %s", after.Format("15:04"), next.Format("2006-01-02 15:04"), expected.Format("2006-01-02 15:04"))
	}
}
