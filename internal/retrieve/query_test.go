package retrieve

import (
	"strings"
	"testing"
)

func TestFTSQuery(t *testing.T) {
	// El exacto siempre está; las stopwords se descartan; la puntuación se limpia.
	t.Run("keeps exact term", func(t *testing.T) {
		got := FTSQuery("supermercado")
		if !strings.Contains(got, `"supermercado"`) {
			t.Errorf("FTSQuery = %q, want it to contain the exact term", got)
		}
	})
	t.Run("OR between terms", func(t *testing.T) {
		got := FTSQuery("profe supermercado")
		if !strings.Contains(got, " OR ") {
			t.Errorf("FTSQuery = %q, want OR between terms", got)
		}
	})
	t.Run("drops stopwords", func(t *testing.T) {
		got := FTSQuery("que hace el profe en el supermercado")
		for _, sw := range []string{`"que"`, `"el"`, `"en"`} {
			if strings.Contains(got, sw) {
				t.Errorf("FTSQuery = %q, should not contain stopword %s", got, sw)
			}
		}
		if !strings.Contains(got, `"profe"`) || !strings.Contains(got, `"supermercado"`) {
			t.Errorf("FTSQuery = %q, want content terms kept", got)
		}
	})
	t.Run("strips punctuation", func(t *testing.T) {
		got := FTSQuery("nan loss, inestable?")
		for _, want := range []string{`"nan"`, `"loss"`, `"inestable"`} {
			if !strings.Contains(got, want) {
				t.Errorf("FTSQuery = %q, want %s", got, want)
			}
		}
	})
	t.Run("all stopwords kept", func(t *testing.T) {
		if got := FTSQuery("de la que"); got == "" {
			t.Error("FTSQuery of only-stopwords = empty, want them kept")
		}
	})
	t.Run("empty", func(t *testing.T) {
		if got := FTSQuery("   "); got != "" {
			t.Errorf("FTSQuery(blank) = %q, want empty", got)
		}
	})
}

// TestFTSQuery_StemPrefix verifica que un término flexionado genera un término de
// prefijo de stem (`"..."*`) además del exacto, para matchear variantes.
func TestFTSQuery_StemPrefix(t *testing.T) {
	for _, word := range []string{"programación", "corriendo", "entrenamiento"} {
		got := FTSQuery(word)
		if !strings.Contains(got, `"`+word+`"`) {
			t.Errorf("FTSQuery(%q) = %q, want exact term present", word, got)
		}
		if !strings.Contains(got, `"*`) {
			t.Errorf("FTSQuery(%q) = %q, want a stem-prefix term (\"...\"*)", word, got)
		}
	}
}

func TestStemPrefixes(t *testing.T) {
	// Un término corto no debe producir prefijo (evita over-matching).
	if got := stemPrefixes("ir"); len(got) != 0 {
		t.Errorf("stemPrefixes(\"ir\") = %v, want none (too short)", got)
	}
	// Un término flexionado largo sí, y debe ser prefijo del término.
	got := stemPrefixes("programación")
	if len(got) == 0 {
		t.Fatal("stemPrefixes(\"programación\") = none, want at least one")
	}
	for _, st := range got {
		if !strings.HasPrefix("programación", st) {
			t.Errorf("stem %q is not a prefix of the word", st)
		}
		if len([]rune(st)) < minStemLen {
			t.Errorf("stem %q shorter than minStemLen", st)
		}
	}
}
