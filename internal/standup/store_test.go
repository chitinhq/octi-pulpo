package standup

import (
	"testing"
)

func TestStatusOf(t *testing.T) {
	tests := []struct {
		name    string
		report  Report
		want    Status
	}{
		{
			name:   "green — has done, no blockers",
			report: Report{Done: []string{"Merged #1"}, Doing: []string{"Working on #2"}},
			want:   StatusGreen,
		},
		{
			name:   "green — doing only, no blockers",
			report: Report{Doing: []string{"Working on #2"}},
			want:   StatusGreen,
		},
		{
			name:   "yellow — done and blocked",
			report: Report{Done: []string{"Merged #1"}, Blocked: []string{"#2 needs review"}},
			want:   StatusYellow,
		},
		{
			name:   "red — blocked, nothing done",
			report: Report{Blocked: []string{"#2 needs review"}},
			want:   StatusRed,
		},
		{
			name:   "red — completely empty",
			report: Report{},
			want:   StatusRed,
		},
		{
			name:   "green — has requests but no blockers",
			report: Report{Done: []string{"Merged #1"}, Requests: []string{"analytics report"}},
			want:   StatusGreen,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StatusOf(&tc.report)
			if got != tc.want {
				t.Errorf("StatusOf(%+v) = %q, want %q", tc.report, got, tc.want)
			}
		})
	}
}
