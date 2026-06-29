package config

import (
	"path/filepath"
	"testing"
)

func TestValidTeamName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"acme", true},
		{"team-1", true},
		{"team_2", true},
		{"", false},
		{"a/b", false},
		{`a\b`, false},
		{"with space", false},
		{"with\ttab", false},
		{".", false},
		{"..", false},
	}
	for _, c := range cases {
		err := ValidTeamName(c.name)
		if c.ok && err != nil {
			t.Errorf("ValidTeamName(%q) = %v, want nil", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("ValidTeamName(%q) = nil, want error", c.name)
		}
	}
}

func TestTeamPaths(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())

	dir, err := TeamDir("acme")
	if err != nil {
		t.Fatalf("TeamDir: %v", err)
	}
	teams, _ := TeamsDir()
	if want := filepath.Join(teams, "acme"); dir != want {
		t.Errorf("TeamDir = %q, want %q", dir, want)
	}

	dbPath, err := TeamDBPath("acme")
	if err != nil {
		t.Fatalf("TeamDBPath: %v", err)
	}
	if want := filepath.Join(dir, "nem.db"); dbPath != want {
		t.Errorf("TeamDBPath = %q, want %q", dbPath, want)
	}

	chats, err := TeamChatsDir("acme")
	if err != nil {
		t.Fatalf("TeamChatsDir: %v", err)
	}
	if want := filepath.Join(dir, "store", "chats"); chats != want {
		t.Errorf("TeamChatsDir = %q, want %q", chats, want)
	}

	if _, err := TeamDir("bad/name"); err == nil {
		t.Error("TeamDir(bad/name) = nil error, want rejection")
	}
}

func TestNemHomeOverridesDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NEM_HOME", home)

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join(home, ".nem"); dir != want {
		t.Errorf("Dir = %q, want %q", dir, want)
	}
}
