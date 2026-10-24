package ingest

import (
	"fmt"
	json "github.com/segmentio/encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dieg0Code/nem/internal/db"
)

// claudeParser implementa Parser para las sesiones de Claude Code
// (~/.claude/projects/<proyecto>/<sessionId>.jsonl).
type claudeParser struct{}

// NewClaudeParser crea el parser de sesiones de Claude Code.
func NewClaudeParser() Parser {
	return &claudeParser{}
}

func (p *claudeParser) Source() string { return "claude" }

func (p *claudeParser) DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// claudeLine es una línea del log de Claude. Solo type user/assistant traen
// message; el resto (system, ai-title, file-history-snapshot, etc.) se ignora.
// IsSidechain marca las líneas de un subagente: traen el sessionId del padre,
// así que ingestarlas mezclaría otra conversación (con su propio seq) en el
// chat padre. El padre ya guarda el informe final del subagente como
// tool_result, de modo que se descartan.
type claudeLine struct {
	Type        string         `json:"type"`
	IsSidechain bool           `json:"isSidechain"`
	Message     *claudeMessage `json:"message"`
	UUID        string         `json:"uuid"`
	Timestamp   string         `json:"timestamp"`
	SessionID   string         `json:"sessionId"`
	Cwd         string         `json:"cwd"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string | []claudeBlock
}

// claudeBlock es un bloque dentro de message.content cuando es un array.
type claudeBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`     // text
	Thinking string          `json:"thinking"` // thinking
	Name     string          `json:"name"`     // tool_use
	Input    json.RawMessage `json:"input"`    // tool_use
	Content  json.RawMessage `json:"content"`  // tool_result (string | array)
}

func (p *claudeParser) Parse(r io.Reader, sessionPath string) (*ParsedChat, error) {
	chat := db.Chat{Source: "claude", SessionPath: sessionPath}
	chatID := strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath))
	var msgs []db.Message
	var seq int64

	// add agrega un mensaje con id estable derivado del uuid de la línea y el
	// índice de bloque (una línea puede expandir en varios mensajes).
	add := func(uuid, role, content string, blockIdx int, ts int64) {
		if strings.TrimSpace(content) == "" {
			return
		}
		seq++
		id := fmt.Sprintf("%s:%d", uuid, blockIdx)
		if uuid == "" {
			id = fmt.Sprintf("%s:%d", chatID, seq)
		}
		msgs = append(msgs, db.Message{
			ID:        id,
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
		var rec claudeLine
		if err := json.Unmarshal(line, &rec); err != nil || rec.IsSidechain {
			continue
		}

		// Capturar metadata del chat desde cualquier línea que la traiga.
		if rec.SessionID != "" {
			chatID = rec.SessionID
			chat.ID = rec.SessionID
		}
		if chat.Title == "" && rec.Cwd != "" {
			chat.Title = titleFromCwd(rec.Cwd)
		}

		if (rec.Type != "user" && rec.Type != "assistant") || rec.Message == nil {
			continue
		}
		role := rec.Message.Role
		if role != "user" && role != "assistant" {
			role = rec.Type
		}
		ts := parseTimestamp(rec.Timestamp)

		// content puede ser un string simple o un array de bloques.
		var text string
		if err := json.Unmarshal(rec.Message.Content, &text); err == nil {
			add(rec.UUID, role, text, 0, ts)
			continue
		}
		var blocks []claudeBlock
		if err := json.Unmarshal(rec.Message.Content, &blocks); err != nil {
			continue
		}
		for i, b := range blocks {
			switch b.Type {
			case "text":
				add(rec.UUID, role, b.Text, i, ts)
			case "thinking":
				add(rec.UUID, RoleReasoning, truncate(b.Thinking, maxReasoningChars), i, ts)
			case "tool_use":
				call := strings.TrimSpace(b.Name + " " + string(b.Input))
				add(rec.UUID, RoleTool, truncate(call, maxToolChars), i, ts)
			case "tool_result":
				add(rec.UUID, RoleTool, truncate(claudeToolResultText(b.Content), maxToolChars), i, ts)
			}
		}
	}
	if err := lines.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan claude session: %w", err)
	}

	if chat.ID == "" {
		chat.ID = chatID
	}
	if chat.CreatedAt == 0 && len(msgs) > 0 {
		chat.CreatedAt = msgs[0].Timestamp
	}
	return &ParsedChat{Chat: chat, Messages: msgs}, nil
}

// claudeToolResultText extrae texto de un tool_result, cuyo content puede ser un
// string o un array de bloques {type:text,text}.
func claudeToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
