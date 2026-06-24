package when

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 6, 23, 10, 30, 0, 0, time.Local)

	tests := []struct {
		in   string
		want time.Time
	}{
		{"today", time.Date(2026, 6, 23, 0, 0, 0, 0, time.Local)},
		{"tomorrow", time.Date(2026, 6, 24, 0, 0, 0, 0, time.Local)},
		{"2026-06-27", time.Date(2026, 6, 27, 0, 0, 0, 0, time.Local)},
		{"2026-06-27 14:00", time.Date(2026, 6, 27, 14, 0, 0, 0, time.Local)},
		{"2026-06-27T14:00", time.Date(2026, 6, 27, 14, 0, 0, 0, time.Local)},
		{"+3d", time.Date(2026, 6, 26, 10, 30, 0, 0, time.Local)},
		{"+2w", time.Date(2026, 7, 7, 10, 30, 0, 0, time.Local)},
		{"+6h", time.Date(2026, 6, 23, 16, 30, 0, 0, time.Local)},
	}
	for _, tc := range tests {
		got, err := Parse(tc.in, now)
		if err != nil {
			t.Errorf("Parse(%q) error = %v", tc.in, err)
			continue
		}
		if got != tc.want.Unix() {
			t.Errorf("Parse(%q) = %v, want %v", tc.in, time.Unix(got, 0), tc.want)
		}
	}
}

func TestParse_Invalid(t *testing.T) {
	now := time.Now()
	for _, in := range []string{"", "someday", "27-06-2026", "+3", "+3y", "+xd"} {
		if _, err := Parse(in, now); err == nil {
			t.Errorf("Parse(%q) = nil error, want error", in)
		}
	}
}

func TestHumanize(t *testing.T) {
	now := time.Date(2026, 6, 23, 10, 0, 0, 0, time.Local).Unix()
	day := func(y, m, d, h int) int64 {
		return time.Date(y, time.Month(m), d, h, 0, 0, 0, time.Local).Unix()
	}
	tests := []struct {
		due  int64
		want string
	}{
		{day(2026, 6, 23, 23), "today"},   // hoy más tarde
		{day(2026, 6, 24, 8), "tomorrow"}, // mañana
		{day(2026, 6, 26, 0), "in 3d"},
		{day(2026, 6, 22, 0), "overdue 1d"},
		{day(2026, 6, 20, 0), "overdue 3d"},
	}
	for _, tc := range tests {
		if got := Humanize(tc.due, now); got != tc.want {
			t.Errorf("Humanize(%v) = %q, want %q", time.Unix(tc.due, 0), got, tc.want)
		}
	}
}
