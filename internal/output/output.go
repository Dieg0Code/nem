// Package output serializa snapshots de commits y renderiza conversaciones en
// los formatos que consumen humanos y agentes (llm, json, markdown).
package output

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
)

// Formato de salida soportado.
const (
	FormatLLM      = "llm"
	FormatJSON     = "json"
	FormatMarkdown = "markdown"
)

// SnapMessage es un mensaje dentro del snapshot inmutable de un commit.
type SnapMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
	Seq       int64  `json:"seq"`
}

// BuildSnapshot serializa los mensajes a JSON para guardarlos inmutables en un
// commit.
func BuildSnapshot(msgs []db.Message) (string, error) {
	snap := make([]SnapMessage, 0, len(msgs))
	for _, m := range msgs {
		snap = append(snap, SnapMessage{
			Role:      m.Role,
			Content:   m.Content,
			Timestamp: m.Timestamp,
			Seq:       m.Seq,
		})
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "", fmt.Errorf("failed to build snapshot: %w", err)
	}
	return string(b), nil
}

// ParseSnapshot deserializa el snapshot JSON de un commit.
func ParseSnapshot(s string) ([]SnapMessage, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var snap []SnapMessage
	if err := json.Unmarshal([]byte(s), &snap); err != nil {
		return nil, fmt.Errorf("failed to parse snapshot: %w", err)
	}
	return snap, nil
}

// Doc es lo que se renderiza: metadata del chat, los mensajes y, opcionalmente,
// el commit que los respalda.
type Doc struct {
	Title    string
	Source   string
	Date     time.Time
	Messages []SnapMessage
	Commit   *db.Commit
	// Origin etiqueta de qué store vino el doc en lectura federada (p.ej.
	// "personal" | "team:acme"). Vacío = no se muestra.
	Origin string
}

// Render produce la representación de doc en el formato pedido.
func Render(doc Doc, format string) (string, error) {
	switch format {
	case "", FormatMarkdown:
		return renderMarkdown(doc), nil
	case FormatLLM:
		return renderLLM(doc), nil
	case FormatJSON:
		return renderJSON(doc)
	default:
		return "", fmt.Errorf("unknown format %q (use llm|json|markdown)", format)
	}
}

// renderLLM produce salida limpia para ingestión por un agente: sin metadata de
// ruido, roles como prefijo, y el commit al pie.
func renderLLM(doc Doc) string {
	var b strings.Builder
	if doc.Origin != "" {
		fmt.Fprintf(&b, "[%s | %s | %s | %s]\n\n", doc.Origin, doc.Date.Format("2006-01-02"), doc.Source, doc.Title)
	} else {
		fmt.Fprintf(&b, "[%s | %s | %s]\n\n", doc.Date.Format("2006-01-02"), doc.Source, doc.Title)
	}
	for _, m := range doc.Messages {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	if doc.Commit != nil {
		fmt.Fprintf(&b, "\n— commit %s%s: %q\n", short(doc.Commit.Hash), byAuthor(doc.Commit.Author), doc.Commit.Message)
	}
	return b.String()
}

// byAuthor formatea la atribución de un commit (" by <author>"), vacío si no hay.
func byAuthor(author string) string {
	if author == "" {
		return ""
	}
	return " by " + author
}

// renderMarkdown produce salida legible para humanos.
func renderMarkdown(doc Doc) string {
	var b strings.Builder
	title := doc.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if doc.Origin != "" {
		fmt.Fprintf(&b, "_%s · %s · %s_\n\n", doc.Origin, doc.Source, doc.Date.Format("2006-01-02 15:04"))
	} else {
		fmt.Fprintf(&b, "_%s · %s_\n\n", doc.Source, doc.Date.Format("2006-01-02 15:04"))
	}
	if doc.Commit != nil {
		fmt.Fprintf(&b, "**commit %s**%s — %s\n\n", short(doc.Commit.Hash), byAuthor(doc.Commit.Author), doc.Commit.Message)
	}
	for _, m := range doc.Messages {
		fmt.Fprintf(&b, "**%s**\n\n%s\n\n", m.Role, m.Content)
	}
	return b.String()
}

// renderJSON produce salida estructurada.
func renderJSON(doc Doc) (string, error) {
	payload := map[string]any{
		"title":    doc.Title,
		"source":   doc.Source,
		"date":     doc.Date.Format(time.RFC3339),
		"messages": doc.Messages,
	}
	if doc.Origin != "" {
		payload["origin"] = doc.Origin
	}
	if doc.Commit != nil {
		commit := map[string]any{
			"hash":    doc.Commit.Hash,
			"message": doc.Commit.Message,
		}
		if doc.Commit.Author != "" {
			commit["author"] = doc.Commit.Author
		}
		payload["commit"] = commit
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to render json: %w", err)
	}
	return string(b), nil
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
