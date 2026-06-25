package facts

import (
	"testing"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
)

func unix(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local).Unix()
}

func TestClassifyStability(t *testing.T) {
	now := unix(2026, time.June, 25)
	tests := []struct {
		name      string
		content   string
		dueAt     int64
		hasAnchor bool
		want      string
	}{
		{"anchor → core", "edad {age}", 0, true, Core},
		{"marcador core (nací)", "nací en Osorno", 0, false, Core},
		{"marcador core (nombre)", "mi nombre es Diego", 0, false, Core},
		{"due → volatile", "entregar informe", now, false, Volatile},
		{"marcador volatile", "actualmente trabaja en AIEP", 0, false, Volatile},
		{"default → stable", "prefiere español neutro", 0, false, Stable},
		{"core gana a volatile", "mi nombre es X y actualmente Y", 0, false, Core},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyStability(tt.content, tt.dueAt, tt.hasAnchor); got != tt.want {
				t.Errorf("ClassifyStability = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWeightOrdering(t *testing.T) {
	now := unix(2026, time.June, 25)
	pinnedVolatile := db.Fact{Stability: Volatile, Pinned: true}
	core := db.Fact{Stability: Core}
	stable := db.Fact{Stability: Stable}
	volatile := db.Fact{Stability: Volatile}
	stableHot := db.Fact{Stability: Stable, Hits: 50, LastHit: now}
	volatileViral := db.Fact{Stability: Volatile, Hits: 1_000_000, LastHit: now}

	// pin domina todo, incluso un volatile pinneado sobre un core.
	if Weight(pinnedVolatile, now) <= Weight(core, now) {
		t.Error("pinned debe pesar más que core")
	}
	// orden de capas sin pin.
	if !(Weight(core, now) > Weight(stable, now) && Weight(stable, now) > Weight(volatile, now)) {
		t.Error("orden de capas core>stable>volatile roto")
	}
	// hits ordenan dentro de la capa, pero no cruzan a otra capa.
	if Weight(stableHot, now) <= Weight(stable, now) {
		t.Error("más hits debe pesar más dentro de la capa")
	}
	if Weight(stableHot, now) >= Weight(core, now) {
		t.Error("un stable con muchos hits NO debe superar a un core")
	}
	// el uso está acotado: un volatile con un millón de hits sigue por debajo de
	// un stable sin hits (las capas dominan al uso).
	if Weight(volatileViral, now) >= Weight(stable, now) {
		t.Error("hits acotados: un volatile viral NO debe superar a un stable")
	}
}

func TestRenderDerived(t *testing.T) {
	now := unix(2026, time.June, 25)
	birth := unix(1995, time.July, 20) // cumple aún no pasó en junio → 30

	got := Render(db.Fact{Content: "Diego, {age} años", HasAnchor: true, AnchorAt: birth}, now)
	if got != "Diego, 30 años" {
		t.Errorf("Render {age} = %q, want \"Diego, 30 años\"", got)
	}
	// sin anchor, el contenido no se toca (aunque tenga el token).
	plain := db.Fact{Content: "texto {age} literal"}
	if Render(plain, now) != "texto {age} literal" {
		t.Errorf("sin anchor no debe sustituir tokens")
	}
	// anchor pre-1970 (timestamp negativo) también se resuelve.
	pre := db.Fact{Content: "{age} años", HasAnchor: true, AnchorAt: unix(1955, time.March, 3)}
	if got := Render(pre, now); got != "71 años" {
		t.Errorf("Render anchor pre-1970 = %q, want \"71 años\"", got)
	}
	// anchor exactamente en el epoch (AnchorAt=0) es válido gracias a HasAnchor.
	epoch := db.Fact{Content: "{age}", HasAnchor: true, AnchorAt: 0}
	if Render(epoch, now) == "{age}" {
		t.Errorf("anchor epoch (0) debe resolverse, no quedar literal")
	}
}

func TestPresentBudgetAndCollapse(t *testing.T) {
	now := unix(2026, time.June, 25)
	var all []db.Fact
	// 1 core + 1 pinned + 5 stable + 3 volatile = 9 estables.
	all = append(all, db.Fact{ID: "core1", Stability: Core, Content: "core"})
	all = append(all, db.Fact{ID: "pin1", Stability: Volatile, Pinned: true, Content: "pinned"})
	for i := range 5 {
		all = append(all, db.Fact{ID: "s" + string(rune('a'+i)), Stability: Stable, Content: "stable"})
	}
	for i := range 3 {
		all = append(all, db.Fact{ID: "v" + string(rune('a'+i)), Stability: Volatile, Content: "volatile"})
	}
	// + un reminder (no entra al cap).
	all = append(all, db.Fact{ID: "r1", DueAt: unix(2026, time.July, 1), Content: "reminder"})

	res := Present(all, now, 3) // budget=3 NO exentos

	// core + pinned siempre; + 3 del resto = 5 mostrados; 8-3=5 no-exentos, colapsan 5.
	exemptShown, capped := 0, 0
	for _, l := range res.Stable {
		if l.ID == "core1" || l.ID == "pin1" {
			exemptShown++
		} else {
			capped++
		}
	}
	if exemptShown != 2 {
		t.Errorf("core+pinned deben mostrarse siempre, got %d", exemptShown)
	}
	if capped != 3 {
		t.Errorf("deben mostrarse 3 no-exentos (budget), got %d", capped)
	}
	if res.Collapsed != 5 {
		t.Errorf("Collapsed = %d, want 5", res.Collapsed)
	}
	if len(res.Reminders) != 1 || res.Reminders[0].ID != "r1" {
		t.Errorf("el reminder debe ir aparte y no entrar al cap")
	}
}

func TestMatchQuery(t *testing.T) {
	all := []db.Fact{
		{ID: "1", Content: "Diego enseña en AIEP"},
		{ID: "2", Content: "stack Go y Python"},
		{ID: "3", Content: "vive en Osorno"},
	}
	m := MatchQuery(all, "dónde queda Osorno") // "osorno" ≥3, "dónde"/"queda" no matchean nada
	if len(m) != 1 || m[0].ID != "3" {
		t.Fatalf("MatchQuery debió matchear solo el fact 3, got %v", IDs(m))
	}
	// términos cortos no generan match espurio.
	if got := MatchQuery(all, "en"); len(got) != 0 {
		t.Errorf("término corto no debe matchear, got %v", IDs(got))
	}
}
