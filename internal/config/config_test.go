package config

import "testing"

func TestTeamRegistryRoundTrip(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())

	// Empty registry on a fresh home.
	teams, err := Teams()
	if err != nil {
		t.Fatalf("Teams: %v", err)
	}
	if len(teams) != 0 {
		t.Fatalf("Teams = %v, want empty", teams)
	}

	if _, ok, err := GetTeam("acme"); err != nil || ok {
		t.Fatalf("GetTeam(acme) = (_, %v, %v), want (_, false, nil)", ok, err)
	}

	want := Team{URL: "git@example.com:acme.git"}
	if err := AddTeam("acme", want); err != nil {
		t.Fatalf("AddTeam: %v", err)
	}

	got, ok, err := GetTeam("acme")
	if err != nil || !ok {
		t.Fatalf("GetTeam(acme) after add = (_, %v, %v)", ok, err)
	}
	if got != want {
		t.Errorf("GetTeam(acme) = %+v, want %+v", got, want)
	}

	if err := RemoveTeam("acme"); err != nil {
		t.Fatalf("RemoveTeam: %v", err)
	}
	if _, ok, _ := GetTeam("acme"); ok {
		t.Error("GetTeam(acme) after remove = ok, want gone")
	}
	// Removing a missing team is a no-op.
	if err := RemoveTeam("ghost"); err != nil {
		t.Errorf("RemoveTeam(ghost) = %v, want nil", err)
	}
}

func TestAddTeamRejectsBadName(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	if err := AddTeam("bad/name", Team{}); err == nil {
		t.Error("AddTeam(bad/name) = nil, want error")
	}
}

func TestUserName(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())

	// Config explícita gana.
	if err := Set("user.name", "Diego"); err != nil {
		t.Fatalf("Set user.name: %v", err)
	}
	if got := UserName(); got != "Diego" {
		t.Errorf("UserName = %q, want Diego", got)
	}
	if got, _ := Get("user.name"); got != "Diego" {
		t.Errorf("Get user.name = %q, want Diego", got)
	}

	// Sin config, nunca vacío (cae a git/env/unknown).
	t.Setenv("NEM_HOME", t.TempDir())
	if got := UserName(); got == "" {
		t.Error("UserName fallback = empty, want non-empty")
	}
}
