package ingest

import (
	"fmt"
	json "github.com/segmentio/encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Dieg0Code/nem/internal/db"
)

// codexParser implementa Parser para las sesiones de Codex
// (~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl).
type codexParser struct{}

// NewCodexParser crea el parser de sesiones de Codex.
func NewCodexParser() Parser {
	return &codexParser{}
}

func (p *codexParser) Source() string { return "codex" }

func (p *codexParser) DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

// codexLine es el envoltorio de cada línea del rollout.
type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// codexMeta es el payload de la línea session_meta.
type codexMeta struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

// codexContentItem es un fragmento de contenido de un mensaje de Codex.
type codexContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// codexMessage es el payload de un response_item de tipo message.
type codexMessage struct {
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Content []codexContentItem `json:"content"`
}

// codexReasoning es el payload de un response_item de tipo reasoning. El texto
// útil vive en summary[].text (content viene encriptado).
type codexReasoning struct {
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

// codexToolCall cubre function_call y custom_tool_call.
type codexToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // function_call (JSON string)
	Input     string `json:"input"`     // custom_tool_call (p.ej. un patch)
}

// codexToolOutput cubre function_call_output y custom_tool_call_output.
type codexToolOutput struct {
	Output string `json:"output"`
}

// codexUUIDFromName extrae el UUID del nombre del archivo rollout como fallback
// del chat id cuando no hay session_meta.
var codexUUIDFromName = regexp.MustCompile(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)

func (p *codexParser) Parse(r io.Reader, sessionPath string) (*ParsedChat, error) {
	chatID := fallbackChatID(sessionPath)
	chat := db.Chat{ID: chatID, Source: "codex", SessionPath: sessionPath}
	var msgs []db.Message
	var seq int64

	// add agrega un mensaje con id estable (chatID:seq) si el contenido no es vacío.
	add := func(role, content string, ts int64) {
		if strings.TrimSpace(content) == "" {
			return
		}
		seq++
		msgs = append(msgs, db.Message{
			ID:        fmt.Sprintf("%s:%d", chatID, seq),
			ChatID:    chatID,
			Role:      role,
			Content:   content,
			Timestamp: ts,
			Seq:       seq,
		})
	}

	lines := newLineReader(r)

	for lines.Next() {
		line := lines.Bytes()
		var rec codexLine
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // línea corrupta: saltar, no abortar
		}
		ts := parseTimestamp(rec.Timestamp)

		switch rec.Type {
		case "session_meta":
			var meta codexMeta
			if err := json.Unmarshal(rec.Payload, &meta); err != nil {
				continue
			}
			if meta.ID != "" {
				chatID = meta.ID
				chat.ID = meta.ID
			}
			chat.Title = titleFromCwd(meta.Cwd)
			chat.CreatedAt = parseTimestamp(meta.Timestamp)

		case "response_item":
			var head struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(rec.Payload, &head); err != nil {
				continue
			}
			switch head.Type {
			case "message":
				var m codexMessage
				if err := json.Unmarshal(rec.Payload, &m); err != nil {
					continue
				}
				if m.Role != "user" && m.Role != "assistant" {
					continue // developer/system = instrucciones, se ignoran
				}
				add(m.Role, joinCodexText(m.Content), ts)

			case "reasoning":
				var rz codexReasoning
				if err := json.Unmarshal(rec.Payload, &rz); err != nil {
					continue
				}
				add(RoleReasoning, joinCodexReasoning(rz), ts)

			case "function_call", "custom_tool_call":
				var t codexToolCall
				if err := json.Unmarshal(rec.Payload, &t); err != nil {
					continue
				}
				add(RoleTool, compactCodexToolCall(t), ts)

			case "function_call_output", "custom_tool_call_output":
				var o codexToolOutput
				if err := json.Unmarshal(rec.Payload, &o); err != nil {
					continue
				}
				add(RoleTool, truncate(o.Output, maxToolChars), ts)
			}
		}
	}
	if err := lines.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan codex session: %w", err)
	}

	// Si nunca hubo session_meta, los mensajes ya usan el chatID de fallback.
	if chat.CreatedAt == 0 && len(msgs) > 0 {
		chat.CreatedAt = msgs[0].Timestamp
	}
	return &ParsedChat{Chat: chat, Messages: msgs}, nil
}

// joinCodexText concatena los fragmentos de texto de un mensaje de Codex.
func joinCodexText(content []codexContentItem) string {
	var parts []string
	for _, it := range content {
		if strings.TrimSpace(it.Text) != "" {
			parts = append(parts, it.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// joinCodexReasoning arma el texto del razonamiento desde summary[], acotado.
func joinCodexReasoning(rz codexReasoning) string {
	var parts []string
	for _, s := range rz.Summary {
		if strings.TrimSpace(s.Text) != "" {
			parts = append(parts, s.Text)
		}
	}
	return truncate(strings.Join(parts, "\n"), maxReasoningChars)
}

// compactCodexToolCall arma una línea compacta "<name> <args>" para una llamada
// a herramienta, truncada para no inflar la DB ni el índice.
func compactCodexToolCall(t codexToolCall) string {
	arg := t.Arguments
	if arg == "" {
		arg = t.Input
	}
	s := t.Name
	if arg != "" {
		s = strings.TrimSpace(s + " " + arg)
	}
	return truncate(s, maxToolChars)
}

// fallbackChatID deriva un id estable del nombre del archivo de sesión.
func fallbackChatID(sessionPath string) string {
	base := filepath.Base(sessionPath)
	if m := codexUUIDFromName.FindString(base); m != "" {
		return m
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}
