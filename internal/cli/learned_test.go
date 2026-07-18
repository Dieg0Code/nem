package cli

import (
	"testing"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/output"
	"github.com/spf13/cobra"
)

// TestUsageSignalNeverTouchesTeamStore pinnea el invariante de privacidad de la
// señal aprendida: una búsqueda federada loguea SOLO en el store personal, y
// leer un commit de un team store no registra señal en ninguna parte.
func TestUsageSignalNeverTouchesTeamStore(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())

	dbPath, _ := config.DBPath()
	seedMsg(t, dbPath, "p1", "personal proj", "deployment pipeline personal note")
	hash := seedTeam(t, "acme", "deployment runbook for the whole team")

	// Búsqueda federada: sirve hits de ambos stores.
	runCmd(t, func(cmd *cobra.Command) error {
		return runSearch(cmd, "deployment", 10, output.FormatMarkdown, "", "hybrid", true, "", false)
	})
	// Read del commit del team (se resuelve federado en el store acme).
	runCmd(t, func(cmd *cobra.Command) error {
		return runRead(cmd, "", hash, output.FormatLLM, "")
	})

	// El store personal tiene el log de la búsqueda; el team, NADA.
	personal, err := db.New(db.WithPath(dbPath))
	if err != nil {
		t.Fatalf("open personal: %v", err)
	}
	defer personal.Close()
	logs, err := personal.RecentSearches(0)
	if err != nil {
		t.Fatalf("personal RecentSearches: %v", err)
	}
	if len(logs) != 1 {
		t.Errorf("expected 1 search log in the personal store, got %d", len(logs))
	}

	teamDB, _ := config.TeamDBPath("acme")
	team, err := db.New(db.WithPath(teamDB))
	if err != nil {
		t.Fatalf("open team: %v", err)
	}
	defer team.Close()
	teamLogs, err := team.RecentSearches(0)
	if err != nil {
		t.Fatalf("team RecentSearches: %v", err)
	}
	if len(teamLogs) != 0 {
		t.Errorf("team store must have zero search logs, got %d", len(teamLogs))
	}
	// El read del commit del team no debe haber aprendido nada en ningún store
	// (el par query→read solo existe para reads del store personal).
	for name, s := range map[string]db.Store{"personal": personal, "team": team} {
		m, err := s.MatchNodeTerms([]string{"deployment", "deploy", "runbook"}, 10)
		if err != nil {
			t.Fatalf("%s MatchNodeTerms: %v", name, err)
		}
		if len(m) != 0 {
			t.Errorf("%s store must have no learned pairs after a team read, got %+v", name, m)
		}
	}
}
