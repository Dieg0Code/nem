package facts

import (
	"strings"
	"testing"
)

// TestRevisionHint cubre el canario anti-cámara-de-eco: solo advierte cuando el
// store es maduro Y hace mucho que nada se supersede.
func TestRevisionHint(t *testing.T) {
	const day = int64(86400)
	now := int64(1_000_000_000)

	cases := []struct {
		name     string
		lastRev  int64
		since    int64
		wantWarn bool
	}{
		{"store vacío", 0, 0, false},
		{"store joven sin revisiones", 0, now - 10*day, false},
		{"maduro, nunca revisado", 0, now - 90*day, true},
		{"maduro, revisión reciente", now - 5*day, now - 90*day, false},
		{"maduro, revisión vieja", now - 70*day, now - 400*day, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RevisionHint(c.lastRev, c.since, now)
			if (got != "") != c.wantWarn {
				t.Errorf("RevisionHint(%d, %d) = %q, wantWarn=%v", c.lastRev, c.since, got, c.wantWarn)
			}
		})
	}

	// El mensaje distingue "nunca" de "hace N días".
	never := RevisionHint(0, now-90*day, now)
	if !strings.Contains(never, "since this store began") {
		t.Errorf("never-revised hint should say so: %q", never)
	}
	old := RevisionHint(now-70*day, now-400*day, now)
	if !strings.Contains(old, "in 70d") {
		t.Errorf("stale hint should carry the age: %q", old)
	}
}
