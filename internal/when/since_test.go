package when

import (
	"testing"
	"time"
)

func at(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local).Unix()
}

func TestYearsSince(t *testing.T) {
	birth := at(1995, time.July, 20)
	tests := []struct {
		name string
		now  int64
		want int
	}{
		{"antes del cumple", at(2026, time.June, 25), 30},
		{"el día del cumple", at(2026, time.July, 20), 31},
		{"un día antes", at(2026, time.July, 19), 30},
		{"después del cumple", at(2026, time.August, 1), 31},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := YearsSince(birth, tt.now); got != tt.want {
				t.Errorf("YearsSince = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHumanizeSince(t *testing.T) {
	now := at(2026, time.June, 25)
	tests := []struct {
		name   string
		anchor int64
		want   string
	}{
		{"hoy", at(2026, time.June, 25), "hoy"},
		{"ayer", at(2026, time.June, 24), "ayer"},
		{"días", at(2026, time.June, 20), "hace 5d"},
		{"meses", at(2026, time.April, 20), "hace 2 meses"},
		{"años", at(2020, time.January, 1), "hace 6 años"},
		{"futuro → hoy", at(2026, time.July, 1), "hoy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HumanizeSince(tt.anchor, now); got != tt.want {
				t.Errorf("HumanizeSince = %q, want %q", got, tt.want)
			}
		})
	}
}
