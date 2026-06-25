package ingest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dieg0Code/nem/internal/db"
)

// antigravityParser implementa Parser para las sesiones del CLI de Google
// Antigravity. El CLI guarda cada conversación como un transcript JSONL en
// ~/.gemini/antigravity-cli/brain/<conversation-id>/.system_generated/logs/transcript.jsonl
// El desktop guarda en protobuf (conversations/<id>.pb) y queda fuera de alcance.
type antigravityParser struct{}

// NewAntigravityParser crea el parser de sesiones del CLI de Antigravity.
func NewAntigravityParser() Parser { return &antigravityParser{} }

func (p *antigravityParser) Source() string { return "antigravity" }

func (p *antigravityParser) DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain"), nil
}

// antigravityTranscriptName es el único .jsonl de cada conversación que nos
// interesa. El walker compartido también encuentra transcript_full.jsonl (lo
// mismo + ruido de tools) e history.jsonl (índice): ambos se ignoran.
const antigravityTranscriptName = "transcript.jsonl"

// antigravityLine es una línea del transcript del CLI de Antigravity.
type antigravityLine struct {
	StepIndex int                   `json:"step_index"`
	Source    string                `json:"source"` // USER_EXPLICIT | MODEL | SYSTEM
	Type      string                `json:"type"`   // USER_INPUT | PLANNER_RESPONSE | VIEW_FILE | ...
	CreatedAt string                `json:"created_at"`
	Content   string                `json:"content"`
	ToolCalls []antigravityToolCall `json:"tool_calls"`
}

// antigravityToolCall es una llamada a herramienta dentro de un PLANNER_RESPONSE.
type antigravityToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

func (p *antigravityParser) Parse(r io.Reader, sessionPath string) (*ParsedChat, error) {
	// El walker compartido entrega todos los .jsonl bajo el root; solo parseamos
	// el transcript canónico de cada conversación (los demás se cuentan skipped).
	if !strings.EqualFold(filepath.Base(sessionPath), antigravityTranscriptName) {
		return &ParsedChat{}, nil
	}

	chatID := antigravityConvID(sessionPath)
	chat := db.Chat{ID: chatID, Source: "antigravity", SessionPath: sessionPath}
	var msgs []db.Message
	var seq int64

	// add agrega un mensaje con id estable (chatID:step:block) si no es vacío.
	add := func(role, content string, step, blockIdx int, ts int64) {
		if strings.TrimSpace(content) == "" {
			return
		}
		seq++
		msgs = append(msgs, db.Message{
			ID:        fmt.Sprintf("%s:%d:%d", chatID, step, blockIdx),
			ChatID:    chatID,
			Role:      role,
			Content:   content,
			Timestamp: ts,
			Seq:       seq,
		})
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec antigravityLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // línea corrupta: saltar, no abortar
		}
		ts := parseTimestamp(rec.CreatedAt)

		switch rec.Source {
		case "USER_EXPLICIT":
			add(RoleUser, antigravityUserText(rec.Content), rec.StepIndex, 0, ts)

		case "MODEL":
			if rec.Type == "PLANNER_RESPONSE" {
				// Narración del modelo (block 0) + sus llamadas a herramientas.
				add(RoleAssistant, rec.Content, rec.StepIndex, 0, ts)
				for i, tc := range rec.ToolCalls {
					add(RoleTool, compactAntigravityToolCall(tc), rec.StepIndex, i+1, ts)
					if chat.Title == "" {
						chat.Title = antigravityTitleFromArgs(tc.Args)
					}
				}
			} else {
				// VIEW_FILE / LIST_DIRECTORY / GREP_SEARCH / ...: salida de tool.
				add(RoleTool, truncate(rec.Content, maxToolChars), rec.StepIndex, 0, ts)
			}

			// SYSTEM (CONVERSATION_HISTORY, ERROR_MESSAGE) se ignora.
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan antigravity session: %w", err)
	}

	if chat.CreatedAt == 0 && len(msgs) > 0 {
		chat.CreatedAt = msgs[0].Timestamp
	}
	return &ParsedChat{Chat: chat, Messages: msgs}, nil
}

// antigravityConvID deriva el id de conversación del path del transcript:
// .../brain/<conversation-id>/.system_generated/logs/transcript.jsonl
func antigravityConvID(sessionPath string) string {
	dir := filepath.Dir(sessionPath)       // .../logs
	dir = filepath.Dir(dir)                // .../.system_generated
	id := filepath.Base(filepath.Dir(dir)) // <conversation-id>
	if id == "" || id == "." || id == string(filepath.Separator) {
		return strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath))
	}
	return id
}

// antigravityUserText extrae el texto real del usuario, desenvolviendo el bloque
// <USER_REQUEST>…</USER_REQUEST> y descartando la metadata que el CLI adjunta
// (<ADDITIONAL_METADATA>, <USER_SETTINGS_CHANGE>).
func antigravityUserText(content string) string {
	const open, closeTag = "<USER_REQUEST>", "</USER_REQUEST>"
	if i := strings.Index(content, open); i >= 0 {
		if j := strings.Index(content, closeTag); j > i {
			return strings.TrimSpace(content[i+len(open) : j])
		}
	}
	return strings.TrimSpace(content)
}

// compactAntigravityToolCall arma "<name> <args>" compacto y truncado.
func compactAntigravityToolCall(tc antigravityToolCall) string {
	s := tc.Name
	if len(tc.Args) > 0 {
		s = strings.TrimSpace(s + " " + string(tc.Args))
	}
	return truncate(s, maxToolChars)
}

// antigravityTitleFromArgs deriva un título legible del workspace a partir del
// DirectoryPath de una tool call (la primera suele ser un listado del workspace
// raíz). El valor viene como string entre comillas; se limpia antes de tomar el
// último segmento.
func antigravityTitleFromArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	raw, ok := m["DirectoryPath"]
	if !ok {
		return ""
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	v = strings.Trim(strings.TrimSpace(v), `"`)
	return titleFromCwd(v)
}
