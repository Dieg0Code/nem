// Package facts contiene la lógica pura de la capa semántica siempre-cargada:
// clasificación de volatilidad, peso (híbrido pin+uso), derivación de valores
// variables (edad desde una fecha invariante) y selección por presupuesto. Es
// el presentador único que comparten `nem outline` y el servidor MCP, para que
// la lógica viva en un solo lugar y sea testeable sin tocar la DB ni la salida.
package facts

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/when"
)

// Clases de estabilidad (volatilidad inferida). Determinan las capas: core y los
// pinned nunca se colapsan; volatile es lo primero en colapsar bajo presupuesto.
const (
	Core     = "core"
	Stable   = "stable"
	Volatile = "volatile"
)

// DefaultBudget es el tope por defecto de facts estables siempre-cargados.
const DefaultBudget = 7

// coreMarkers / volatileMarkers son señales léxicas para inferir la capa. Son
// heurísticas a propósito simples: el objetivo es no obligar al usuario a pensar
// al guardar, no clasificar perfecto (siempre queda el override --stability).
var coreMarkers = []string{
	"nací", "naci", "born", "fecha de nacimiento", "nacimiento", "birth",
	"me llamo", "mi nombre", "nombre:", "nacionalidad", "rut",
}

var volatileMarkers = []string{
	"actualmente", "ahora", "por ahora", "por el momento", "hasta nuevo aviso",
	"esta semana", "este mes", "estos días", "estos dias", "hoy en día",
	"currently", "right now", "this week", "this month", "for now",
}

// ClassifyStability infiere la capa de un fact a partir de su contenido y fechas.
// Orden de precedencia: identidad invariante (anchor o marcadores core) → core;
// estado actual (marcadores volátiles o fecha) → volatile; resto → stable. La
// presencia del anchor se pasa explícita (hasAnchor) porque AnchorAt=0 es una
// fecha válida (1970-01-01) y no un "sin valor".
func ClassifyStability(content string, dueAt int64, hasAnchor bool) string {
	if hasAnchor {
		return Core
	}
	lower := strings.ToLower(content)
	for _, m := range coreMarkers {
		if strings.Contains(lower, m) {
			return Core
		}
	}
	if dueAt > 0 {
		return Volatile
	}
	for _, m := range volatileMarkers {
		if strings.Contains(lower, m) {
			return Volatile
		}
	}
	return Stable
}

// stabilityOf devuelve la capa efectiva de un fact ya guardado, tolerando filas
// viejas sin el campo (NULL/"" → stable).
func stabilityOf(f db.Fact) string {
	switch f.Stability {
	case Core, Volatile:
		return f.Stability
	default:
		return Stable
	}
}

// Las capas se separan por órdenes de magnitud para que su orden domine al uso.
// maxUsage acota el aporte de los hits MUY por debajo del salto entre capas
// (stable−volatile ≈ 1e4) para que un volatile muy usado nunca supere a un
// stable: el uso ordena DENTRO de la capa, no entre capas.
const (
	tierCore     = 1e8
	tierStable   = 1e4
	tierVolatile = 1e0
	pinBoost     = 1e12
	maxUsage     = 1e3
)

// tierBase devuelve el piso de peso de una capa.
func tierBase(stability string) float64 {
	switch stability {
	case Core:
		return tierCore
	case Volatile:
		return tierVolatile
	default: // stable
		return tierStable
	}
}

// Weight computa el peso (no se guarda). Pin → tope absoluto; luego la capa; y
// dentro de la capa, el uso (hits) atenuado por recencia y ACOTADO a maxUsage
// para no cruzar de capa. Empate fino por edad se resuelve en el sort.
func Weight(f db.Fact, now int64) float64 {
	w := tierBase(stabilityOf(f))
	if f.Pinned {
		w += pinBoost
	}
	w += math.Min(float64(f.Hits)*recency(f.LastHit, now), maxUsage)
	return w
}

// recency atenúa el aporte del uso según cuán viejo es el último hit: 1.0 si es
// reciente o nunca tuvo hit (neutral), decayendo hasta un piso de 0.3 (~180d).
func recency(lastHit, now int64) float64 {
	if lastHit <= 0 {
		return 1.0
	}
	days := float64(now-lastHit) / 86400.0
	f := 1.0 - days/180.0
	return math.Max(0.3, math.Min(1.0, f))
}

// alwaysShow indica si un fact está exento del presupuesto (nunca se colapsa).
func alwaysShow(f db.Fact) bool {
	return f.Pinned || stabilityOf(f) == Core
}

// Render sustituye los tokens derivados ({age}, {elapsed}) usando AnchorAt. Si el
// fact no es derivado (AnchorAt=0) devuelve el contenido tal cual.
func Render(f db.Fact, now int64) string {
	if !f.HasAnchor {
		return f.Content
	}
	c := f.Content
	if strings.Contains(c, "{age}") {
		c = strings.ReplaceAll(c, "{age}", strconv.Itoa(when.YearsSince(f.AnchorAt, now)))
	}
	if strings.Contains(c, "{elapsed}") {
		c = strings.ReplaceAll(c, "{elapsed}", when.HumanizeSince(f.AnchorAt, now))
	}
	return c
}

// MatchQuery devuelve los facts cuyo contenido matchea algún término relevante
// de la query (case-insensitive, términos de ≥3 chars). Es la base de la señal
// "aprendida" del peso: como los facts son un conjunto chico, basta una pasada
// en memoria en cada search, sin tocar FTS5. No muta nada.
func MatchQuery(all []db.Fact, query string) []db.Fact {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	var out []db.Fact
	for _, f := range all {
		lc := strings.ToLower(f.Content)
		for _, t := range terms {
			if strings.Contains(lc, t) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// queryTerms parte la query en términos en minúscula de ≥3 caracteres, sin
// duplicados — suficiente para evitar matches espurios con stopwords cortas.
func queryTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9') &&
			!('à' <= r && r <= 'ÿ') // letras acentuadas comunes
	})
	seen := map[string]bool{}
	var terms []string
	for _, f := range fields {
		if len([]rune(f)) < 3 || seen[f] {
			continue
		}
		seen[f] = true
		terms = append(terms, f)
	}
	return terms
}

// IDs extrae los ids de una lista de facts (para RecordFactHit).
func IDs(fs []db.Fact) []string {
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	return ids
}

// Line es una entrada lista para imprimir: contenido ya derivado + id completo
// (la surface hace el short-hash). Due viene humanizado solo para recordatorios.
type Line struct {
	Content string
	ID      string
	Due     string // "" para estables; "in 3d"/"overdue 2d" para reminders
}

// Result es lo que el presentador entrega a cada surface (CLI / MCP).
type Result struct {
	Stable    []Line // facts estables seleccionados, en orden de peso
	Reminders []Line // recordatorios, ordenados por fecha
	Collapsed int    // estables ocultos por presupuesto (para el "+N más")
}

// Present selecciona y ordena la capa siempre-cargada. Separa estables de
// recordatorios; ordena estables por peso y aplica el presupuesto (core+pinned
// exentos; el resto se capa a budget y el sobrante se cuenta en Collapsed);
// ordena recordatorios por fecha. Resuelve los tokens derivados al vuelo.
func Present(all []db.Fact, now int64, budget int) Result {
	if budget <= 0 {
		budget = DefaultBudget
	}
	var stable, reminders []db.Fact
	for _, f := range all {
		if f.DueAt > 0 {
			reminders = append(reminders, f)
		} else {
			stable = append(stable, f)
		}
	}

	sort.SliceStable(stable, func(i, j int) bool {
		wi, wj := Weight(stable[i], now), Weight(stable[j], now)
		if wi != wj {
			return wi > wj
		}
		return stable[i].CreatedAt > stable[j].CreatedAt // empate: más nuevo primero
	})

	var res Result
	shownCapped := 0 // cuenta solo los NO exentos ya mostrados
	for _, f := range stable {
		if alwaysShow(f) {
			res.Stable = append(res.Stable, Line{Content: Render(f, now), ID: f.ID})
			continue
		}
		if shownCapped < budget {
			res.Stable = append(res.Stable, Line{Content: Render(f, now), ID: f.ID})
			shownCapped++
			continue
		}
		res.Collapsed++
	}

	sort.SliceStable(reminders, func(i, j int) bool { return reminders[i].DueAt < reminders[j].DueAt })
	for _, f := range reminders {
		res.Reminders = append(res.Reminders, Line{
			Content: Render(f, now),
			ID:      f.ID,
			Due:     when.Humanize(f.DueAt, now),
		})
	}
	return res
}
