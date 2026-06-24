package retrieve

import (
	"strings"

	"github.com/kljensen/snowball"
)

// minStemLen es el largo mínimo de un stem para usarlo como prefijo de búsqueda.
// Evita prefijos demasiado cortos (p.ej. "ir", "ser") que matchearían de más.
const minStemLen = 4

// stemLangs son los idiomas que stemeamos: el corpus es mixto ES/EN.
var stemLangs = []string{"spanish", "english"}

// stopwords son palabras muy frecuentes (ES+EN) que no aportan señal de
// búsqueda. Se descartan del query para que no inflen el match ni dominen el
// ranking. Si TODO el query son stopwords, se usan igual (mejor algo que nada).
var stopwords = map[string]bool{
	// español
	"de": true, "la": true, "el": true, "los": true, "las": true, "un": true,
	"una": true, "unos": true, "unas": true, "y": true, "o": true, "que": true,
	"en": true, "a": true, "con": true, "por": true, "para": true, "del": true,
	"al": true, "se": true, "su": true, "sus": true, "lo": true, "le": true,
	"les": true, "mi": true, "me": true, "te": true, "tu": true, "es": true,
	"son": true, "como": true, "mas": true, "pero": true, "ya": true, "no": true,
	"si": true, "esto": true, "esta": true, "este": true, "eso": true, "esa": true,
	"ese": true, "hay": true, "muy": true, "sin": true, "sobre": true,
	// inglés
	"the": true, "an": true, "of": true, "to": true, "in": true, "on": true,
	"and": true, "or": true, "is": true, "are": true, "for": true, "with": true,
	"that": true, "this": true, "it": true, "be": true, "as": true, "at": true,
	"by": true, "i": true, "we": true, "you": true,
}

// tokenize normaliza un texto libre a tokens de búsqueda: minúsculas, sin
// comillas ni puntuación de borde. No descarta stopwords (eso lo deciden los
// llamadores). Reutilizado por FTSQuery y por el PRF.
func tokenize(raw string) []string {
	fields := strings.Fields(raw)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.ReplaceAll(f, `"`, ""))
		f = strings.Trim(f, ".,;:!?¿¡()[]{}\"'`*")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// stemPrefixes devuelve los stems (ES+EN) de un token que sirven como PREFIJO
// limpio: largo >= minStemLen, distinto del token y efectivamente prefijo suyo.
// Solo así ensanchar por prefijo es seguro (un stem que no es prefijo —p.ej. un
// irregular— se descarta y el token exacto igual queda en la query).
func stemPrefixes(token string) []string {
	var out []string
	seen := map[string]bool{}
	for _, lang := range stemLangs {
		st, err := snowball.Stem(token, lang, false)
		if err != nil || st == "" {
			continue
		}
		st = strings.ToLower(st)
		if seen[st] || st == token {
			continue
		}
		if len([]rune(st)) >= minStemLen && strings.HasPrefix(token, st) {
			seen[st] = true
			out = append(out, st)
		}
	}
	return out
}

// FTSQuery convierte un query en lenguaje natural en una query FTS5 robusta:
// tokeniza, descarta stopwords y, por cada término, emite el literal exacto MÁS
// el prefijo de su stem (`"stem"*`) para matchear variantes morfológicas
// (programación↔programar) en ambas direcciones. Todo se combina con OR y BM25
// rankea: el documento que matchea más términos (y más raros) sube solo. El
// término exacto sigue presente, así que el match preciso no se pierde. Devuelve
// "" si no queda ningún token utilizable.
func FTSQuery(raw string) string {
	toks := tokenize(raw)
	kept := make([]string, 0, len(toks))
	for _, t := range toks {
		if !stopwords[t] {
			kept = append(kept, t)
		}
	}
	// Si todo eran stopwords, no descartamos: mejor buscar con ellas que nada.
	if len(kept) == 0 {
		kept = toks
	}

	seen := map[string]bool{}
	var terms []string
	add := func(term string) {
		if term != "" && !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	for _, t := range kept {
		add(`"` + t + `"`)
		for _, st := range stemPrefixes(t) {
			add(`"` + st + `"*`)
		}
	}
	return strings.Join(terms, " OR ")
}
