// Package when parsea y humaniza fechas para los recordatorios de nem, en Go
// puro y sin dependencias. Acepta fechas absolutas (ISO) y relativas (+3d,
// today, tomorrow) — el agente normalmente pasa fechas absolutas que ya calculó.
package when

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// FormatNow describe el instante t en lenguaje legible para situar al agente en
// el tiempo: día de la semana, fecha, hora y offset de zona. nem rastrea tiempo
// (recencia, reminders) pero el agente no sabe la hora actual hasta leer esto.
func FormatNow(t time.Time) string {
	return t.Format("Monday 2006-01-02 15:04 (-07:00)")
}

// Parse convierte una expresión de fecha en un unix timestamp (segundos),
// relativo a now y en la zona local de now. Formatos:
//
//	2006-01-02            fecha (a las 00:00 local)
//	2006-01-02 15:04      fecha y hora
//	2006-01-02T15:04      idem (ISO con T)
//	today | tomorrow      atajos de día
//	+3d | +2w | +6h       offset desde now (días, semanas, horas)
func Parse(s string, now time.Time) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty date")
	}
	loc := now.Location()

	switch strings.ToLower(s) {
	case "today":
		return startOfDay(now).Unix(), nil
	case "tomorrow":
		return startOfDay(now).AddDate(0, 0, 1).Unix(), nil
	}

	if strings.HasPrefix(s, "+") {
		return parseOffset(s, now)
	}

	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("unrecognized date %q (use YYYY-MM-DD, +3d, today, tomorrow)", s)
}

// parseOffset interpreta "+3d", "+2w", "+6h" como now + duración.
func parseOffset(s string, now time.Time) (int64, error) {
	body := strings.TrimPrefix(s, "+")
	if len(body) < 2 {
		return 0, fmt.Errorf("bad offset %q", s)
	}
	unit := body[len(body)-1]
	n, err := strconv.Atoi(body[:len(body)-1])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad offset %q (use +3d, +2w, +6h)", s)
	}
	switch unit {
	case 'd':
		return now.AddDate(0, 0, n).Unix(), nil
	case 'w':
		return now.AddDate(0, 0, 7*n).Unix(), nil
	case 'h':
		return now.Add(time.Duration(n) * time.Hour).Unix(), nil
	default:
		return 0, fmt.Errorf("bad offset unit in %q (use d, w, h)", s)
	}
}

// Humanize describe un due relativo a now en lenguaje natural y compacto, por
// día calendario: "today", "tomorrow", "in 3 days", "overdue 2d".
func Humanize(due, now int64) string {
	days := dayDiff(due, now)
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days == -1:
		return "overdue 1d"
	case days > 1:
		return fmt.Sprintf("in %dd", days)
	default: // days < -1
		return fmt.Sprintf("overdue %dd", -days)
	}
}

// YearsSince devuelve los años completos transcurridos entre anchor y now (p.ej.
// la edad a partir de la fecha de nacimiento). No cuenta el año en curso hasta
// que pasa el día del aniversario. Negativo si anchor está en el futuro.
func YearsSince(anchor, now int64) int {
	a := time.Unix(anchor, 0)
	n := time.Unix(now, 0)
	years := n.Year() - a.Year()
	// Aún no llegó el aniversario este año → restar uno.
	if n.Month() < a.Month() || (n.Month() == a.Month() && n.Day() < a.Day()) {
		years--
	}
	return years
}

// HumanizeSince describe el tiempo transcurrido desde anchor en lenguaje natural
// y compacto: "hoy", "hace 3 días", "hace 5 meses", "hace 2 años". Espejo de
// Humanize pero mirando al pasado y con granularidad creciente.
func HumanizeSince(anchor, now int64) string {
	days := dayDiff(now, anchor)
	switch {
	case days <= 0:
		return "hoy"
	case days == 1:
		return "ayer"
	case days < 30:
		return fmt.Sprintf("hace %dd", days)
	case days < 365:
		return fmt.Sprintf("hace %d meses", days/30)
	default:
		y := YearsSince(anchor, now)
		if y <= 1 {
			return "hace 1 año"
		}
		return fmt.Sprintf("hace %d años", y)
	}
}

// dayDiff devuelve la diferencia en días calendario (local) entre due y now,
// truncando ambos a medianoche para que "hoy a cualquier hora" sea 0.
func dayDiff(due, now int64) int {
	d := startOfDay(time.Unix(due, 0))
	n := startOfDay(time.Unix(now, 0))
	// Round absorbe los ±1h de DST sin romper en días negativos (overdue).
	return int(math.Round(d.Sub(n).Hours() / 24))
}

// startOfDay trunca t a las 00:00 de su día en su propia zona.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
