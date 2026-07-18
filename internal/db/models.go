package db

// Chat representa una conversación ingestada desde un agente (codex, claude) o
// creada manualmente. Es el contenedor de mensajes y commits.
type Chat struct {
	ID          string `gorm:"primaryKey"`
	Title       string
	Source      string `gorm:"index"` // "codex" | "claude" | "manual"
	CreatedAt   int64  // unix seconds, autocompletado por GORM
	SessionPath string

	Messages []Message `gorm:"foreignKey:ChatID;constraint:OnDelete:CASCADE"`
}

// Message es un turno individual dentro de un chat. El campo Seq da orden
// determinístico dentro del chat (los timestamps pueden colisionar).
type Message struct {
	ID         string `gorm:"primaryKey"`
	ChatID     string `gorm:"index"`
	Role       string // "user" | "assistant" | "tool" | "system"
	Content    string
	Timestamp  int64
	TokenCount int
	Seq        int64 `gorm:"index"` // orden dentro del chat
}

// Commit es un snapshot INMUTABLE de un rango de mensajes. Copia el texto en
// Snapshot (JSON) al momento de commitear, de modo que reingestar o editar
// mensajes nunca altera lo que un commit ya capturó: "tu agente olvida, nem no".
type Commit struct {
	Hash      string `gorm:"primaryKey"`
	ChatID    string `gorm:"index"`
	Branch    string `gorm:"default:main"`
	Message   string // mensaje del commit, escrito por el agente o el humano
	MsgFrom   string // id del primer mensaje del rango
	MsgTo     string // id del último mensaje del rango
	Snapshot  string // JSON con el texto copiado de los mensajes (inmutable)
	CreatedAt int64
	// Author identifica a quién atribuir el commit en stores compartidos (de
	// config user.name). Metadata: NO entra al hash. Aditivo; filas viejas "".
	Author string
}

// Staging es el index git-like: los mensajes marcados con `nem add` que esperan
// ser commiteados. Una fila por mensaje staged del chat activo.
type Staging struct {
	ID        string `gorm:"primaryKey"`
	ChatID    string `gorm:"index;uniqueIndex:idx_staging_chat_msg"`
	MsgID     string `gorm:"uniqueIndex:idx_staging_chat_msg"`
	Seq       int64  // copia del Seq del mensaje para ordenar el rango
	CreatedAt int64
}

// Memory es la capa MUTABLE sobre el registro inmutable: un hecho/decisión
// destilado que el agente lee al empezar una sesión. Puede actualizarse, y
// referencia el commit que lo respalda como evidencia.
type Memory struct {
	ID         string `gorm:"primaryKey"`
	ChatID     string `gorm:"index"`
	Content    string
	CommitHash string // commit que respalda este recuerdo (evidencia)
	CreatedAt  int64
	UpdatedAt  int64
}

// Fact es la capa de memoria SEMÁNTICA de nem: una afirmación durable que el
// agente (o el humano) escribe directamente, NO derivada de una conversación.
// Vive en un tier privilegiado: se carga SIEMPRE al arrancar (encabeza `nem
// outline`), no se recupera por probabilidad como los nodos/mensajes — por eso
// "quién soy / dónde trabajo / mi rutina" nunca queda enterrado entre 97k
// mensajes. Es append-only en espíritu: una afirmación no se pisa, se REEMPLAZA
// (Superseded/SupersededBy) dejando rastro, igual que un commit.
//
// Kind tipifica la afirmación: "note" (hecho estable, siempre vigente),
// "reminder" (con fecha, vence y se completa) o "schedule" (rutina, texto
// estable). El mismo primitivo cubre memoria semántica y prospectiva: un
// reminder es un Fact con DueAt en el futuro.
type Fact struct {
	ID           string `gorm:"primaryKey"`
	Content      string // la afirmación, escrita por el agente/humano
	Kind         string `gorm:"index"` // note (default) | reminder | schedule
	Source       string // quién la afirmó: claude | codex | human
	Author       string // a quién atribuirla en un team store (config user.name)
	CreatedAt    int64
	UpdatedAt    int64
	Superseded   bool   `gorm:"index"` // reemplazada por una afirmación posterior
	SupersededBy string // id del Fact que la reemplaza

	// Capa prospectiva (recordatorios). DueAt=0 → hecho estable sin fecha.
	DueAt  int64 `gorm:"index"` // cuándo es relevante/vence (unix secs)
	Done   bool  `gorm:"index"` // recordatorio completado (deja de aparecer)
	DoneAt int64 // cuándo se marcó completado

	// Capas (peso) y derivación. Aditivos; filas viejas quedan NULL → tratados
	// como defaults vía COALESCE. El "peso" NO se guarda: se computa en lectura
	// desde estos campos (ver internal/facts).
	Stability string `gorm:"index"` // core | stable (default) | volatile — inferido al add
	Pinned    bool   `gorm:"index"` // override manual: nunca se colapsa por presupuesto
	Hits      int64  // veces que el fact fue útil (matcheó un search / fue resuelto)
	LastHit   int64  // unix del último hit (para decaimiento de recencia)
	// Derivación: HasAnchor marca que AnchorAt es válido (presencia explícita, no
	// un sentinel: AnchorAt=0 es 1970-01-01 y los anteriores son negativos).
	HasAnchor bool
	AnchorAt  int64 // fecha invariante para facts derivados (p.ej. nacimiento)
}

// Node es un nodo del árbol de índice (estilo PageIndex) que el agente navega:
// root → project → chat → commit (→ segment). El Summary es lo que el agente lee
// para decidir en qué rama bajar; el rango [MsgFromSeq, MsgToSeq] permite el
// drill-down al contenido real. Es el equivalente a una "tabla de contenidos"
// de todo el historial, sin embeddings.
type Node struct {
	ID           string `gorm:"primaryKey"`
	ParentID     string `gorm:"index"` // padre en el árbol ("" para root)
	Kind         string `gorm:"index"` // root|project|chat|commit|segment
	ChatID       string `gorm:"index"` // chat dueño (chat/commit/segment)
	Title        string //
	Summary      string // lo que el agente lee para navegar
	MsgFromSeq   int64  // rango cubierto (drill-down)
	MsgToSeq     int64  //
	CommitHash   string // si Kind==commit
	CreatedAt    int64  `gorm:"index"` // tiempo de la fuente (orden temporal); se setea explícito
	Tokens       int    // tamaño aprox. del contenido (para presupuestar)
	Superseded   bool   // la decisión fue reemplazada por una posterior
	SupersededBy string // id del nodo/commit que la reemplaza
	Pinned       bool   `gorm:"index"` // resumen escrito por el agente/humano; `index` nunca lo auto-pisa (capa mutable sobre los commits inmutables)

	// Duraciones (capa temporal): tiempo activo real vs span de calendario, para
	// que el agente calibre estimaciones. Se computan en `index` desde los
	// timestamps de los mensajes (ver internal/timing).
	ActiveSecs int64 // tiempo activo (segundos), modelo role-aware
	WallSecs   int64 // span de calendario (último - primer mensaje), segundos
	Sessions   int   // nº de "sesiones" (sentadas separadas por huecos largos)
	LastActive int64 // unix del último mensaje (para recencia)
}

// Embedding es el vector (opcional) de un nodo del índice, guardado como BLOB
// float32. Capa semántica apagada por default; se llena con `nem index` cuando
// hay un backend de embeddings configurado.
type Embedding struct {
	NodeID string `gorm:"primaryKey"`
	Dim    int
	Vec    []byte // float32 little-endian (ver internal/embed.Encode)
}

// SearchLog registra las búsquedas recientes para correlacionarlas con los reads
// que siguen (pares query→read, la señal aprendida del ranking). Es efímera: se
// prunea a 24h en cada escritura. Estado local derivado: NO se exporta en sync.
type SearchLog struct {
	ID        string `gorm:"primaryKey"`
	Query     string
	ServedIDs string // JSON []string: node IDs servidos ("commit:<hash>", "chat:<id>")
	CreatedAt int64  `gorm:"index"`
}

// NodeTerm es una asociación aprendida término→nodo: cuántas veces un read del
// nodo siguió a una búsqueda que contenía el término. Es el equivalente para
// nodos del Hits/LastHit de Fact. El término se guarda ya normalizado (stem),
// y la clave apunta a node IDs determinísticos ("commit:<hash>", "chat:<id>"),
// así la señal sobrevive un rebuild completo del índice (ClearNodes).
type NodeTerm struct {
	Term    string `gorm:"primaryKey"`
	NodeID  string `gorm:"primaryKey;index"`
	Hits    int64
	LastHit int64
}

// models devuelve todos los modelos para AutoMigrate.
func models() []any {
	return []any{
		&Chat{},
		&Message{},
		&Commit{},
		&Staging{},
		&Memory{},
		&Fact{},
		&Node{},
		&Embedding{},
		&SearchLog{},
		&NodeTerm{},
	}
}
