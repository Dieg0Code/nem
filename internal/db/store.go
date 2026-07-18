// Package db es la capa de persistencia de nem: modelos GORM sobre SQLite
// (glebarez/sqlite, Go puro, sin cgo) más una capa FTS5 en SQL crudo para
// búsqueda full-text con ranking BM25.
package db

import (
	"errors"
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store es la interface de acceso a datos de nem. Toda la app depende de esta
// abstracción, no de GORM directamente, para que los comandos sean testeables
// con un mock.
type Store interface {
	// Migrate aplica el esquema (modelos + FTS5). Es idempotente.
	Migrate() error
	// Close libera la conexión subyacente.
	Close() error

	// UpsertChat inserta el chat o actualiza su metadata si ya existe.
	UpsertChat(chat *Chat) error
	// InsertMessages inserta mensajes ignorando los que ya existen (por id).
	// Devuelve cuántos se insertaron realmente (idempotente).
	InsertMessages(msgs []Message) (int64, error)
	// CountMessages cuenta los mensajes de un chat.
	CountMessages(chatID string) (int64, error)
	// LastMessages devuelve los últimos n mensajes del chat por orden Seq,
	// filtrando por roles (vacío/nil = todos). El filtro se aplica ANTES del
	// límite: "los últimos n de los roles elegidos".
	LastMessages(chatID string, n int, roles []string) ([]Message, error)
	// GetChat devuelve un chat por id (nil, nil si no existe).
	GetChat(id string) (*Chat, error)
	// ListChats devuelve todos los chats (para resolver scopes).
	ListChats() ([]Chat, error)
	// MessageByID devuelve un mensaje por id dentro de un chat.
	MessageByID(chatID, msgID string) (*Message, error)
	// MessagesBySeqRange devuelve los mensajes del chat con Seq en [from,to],
	// filtrando por roles (vacío/nil = todos).
	MessagesBySeqRange(chatID string, fromSeq, toSeq int64, roles []string) ([]Message, error)
	// MessageStamps devuelve los (rol, timestamp) válidos del chat en orden de Seq
	// (para medir duración activa sin cargar el contenido).
	MessageStamps(chatID string) ([]Stamp, error)
	// SearchMessages busca full-text (FTS5/BM25) y devuelve los mejores hits.
	// roles filtra por rol de mensaje; chatIDs limita a esos chats (scope).
	// Ambos vacíos/nil = sin filtro.
	SearchMessages(query string, limit int, roles, chatIDs []string) ([]SearchHit, error)

	// StageMessages agrega mensajes al staging del chat (idempotente por msg).
	// Devuelve cuántos quedaron staged nuevos.
	StageMessages(chatID string, msgs []Message) (int64, error)
	// StagedMessages devuelve los mensajes staged del chat, ordenados por Seq.
	StagedMessages(chatID string) ([]Message, error)
	// CountStaged cuenta los mensajes staged del chat.
	CountStaged(chatID string) (int64, error)
	// ClearStaging vacía el staging del chat.
	ClearStaging(chatID string) error

	// CreateCommit persiste un commit inmutable.
	CreateCommit(c *Commit) error
	// HeadCommit devuelve el commit más reciente del chat (nil si no hay).
	HeadCommit(chatID string) (*Commit, error)
	// GetCommit devuelve un commit por hash (acepta prefijo único).
	GetCommit(hash string) (*Commit, error)
	// ListCommits lista los commits del chat, del más nuevo al más viejo.
	ListCommits(chatID string) ([]Commit, error)
	// ListAllCommits lista todos los commits (para exportar en sync).
	ListAllCommits() ([]Commit, error)

	// --- árbol de índice (nodes) ---
	// ClearNodes borra todo el árbol (para un rebuild completo de `nem index`).
	ClearNodes() error
	// UpsertNodes inserta o actualiza nodos por id. Devuelve cuántos escribió.
	UpsertNodes(nodes []Node) (int64, error)
	// GetNode devuelve un nodo por id (nil, nil si no existe).
	GetNode(id string) (*Node, error)
	// SetNodeSummary reescribe el resumen de un nodo y lo marca Pinned (escrito a
	// mano por el agente/humano), de modo que `nem index` no lo regenere.
	SetNodeSummary(id, summary string) error
	// PinnedNodes devuelve los nodos con resumen Pinned (para respetarlos en Build).
	PinnedNodes() ([]Node, error)
	// ChildNodes devuelve los hijos directos de un nodo (orden por CreatedAt).
	ChildNodes(parentID string) ([]Node, error)
	// RootNodes devuelve los nodos raíz (ParentID == "").
	RootNodes() ([]Node, error)
	// CountNodes cuenta los nodos del árbol.
	CountNodes() (int64, error)
	// SearchNodes busca full-text (FTS5/BM25) sobre title+summary de los nodos.
	// chatIDs limita a esos chats (scope); vacío/nil = sin filtro.
	SearchNodes(query string, limit int, chatIDs []string) ([]NodeHit, error)
	// CommitNodes devuelve los nodos de commit de los chats dados, ordenados por
	// CreatedAt ascendente (para la vista temporal `nem timeline`). chatIDs
	// vacío/nil = todos.
	CommitNodes(chatIDs []string) ([]Node, error)

	// --- facts (memoria semántica: tier privilegiado, siempre cargado) ---
	// AddFact persiste una afirmación durable (el llamador fija id/timestamps).
	AddFact(f *Fact) error
	// UpsertFact inserta un fact, o lo reemplaza si ya existe y el entrante es más
	// nuevo (UpdatedAt mayor o igual). Es el merge de facts entre máquinas en sync.
	UpsertFact(f *Fact) error
	// ListFacts devuelve las afirmaciones (newest first). includeInactive=false
	// devuelve solo las vigentes (ni superseded ni done).
	ListFacts(includeInactive bool) ([]Fact, error)
	// GetFact devuelve una afirmación por id (nil, nil si no existe).
	GetFact(id string) (*Fact, error)
	// SupersedeFact marca oldID como reemplazada por newID (append-only).
	SupersedeFact(oldID, newID string, updatedAt int64) error
	// MarkFactDone marca un recordatorio como completado.
	MarkFactDone(id string, doneAt int64) error
	// RecordFactHit suma uso (señal aprendida del peso) a los facts dados.
	RecordFactHit(ids []string, now int64) error
	// SetFactPinned fija/quita el pin manual de un fact.
	SetFactPinned(id string, pinned bool, updatedAt int64) error
	// DeleteFact borra una afirmación de raíz (corrección de errores).
	DeleteFact(id string) error

	// --- señal de uso (pares query→read que alimentan el ranking) ---
	// LogSearch registra una búsqueda servida (para correlacionar con reads) y
	// prunea entradas viejas.
	LogSearch(id, query string, servedIDs []string, now int64) error
	// RecentSearches devuelve las búsquedas desde `since`, más nueva primero.
	RecentSearches(since int64) ([]SearchLog, error)
	// RecordNodeTermHits suma uso a los pares (término, nodo) dados.
	RecordNodeTermHits(terms []string, nodeID string, now int64) error
	// ConsumeServedID marca un nodo de un SearchLog como ya atribuido (dedup).
	ConsumeServedID(logID, nodeID string) error
	// MatchNodeTerms devuelve nodos leídos tras búsquedas con estos términos,
	// por hits agregados desc (excluye superseded).
	MatchNodeTerms(terms []string, limit int) ([]NodeTermMatch, error)
	// RevisionHealth devuelve el último supersede de facts y la edad del store
	// (canario anti-cámara-de-eco; 0 = nunca / vacío).
	RevisionHealth() (lastRevisionAt, storeSince int64, err error)

	// --- embeddings (capa opcional) ---
	// ClearEmbeddings borra todos los vectores.
	ClearEmbeddings() error
	// UpsertEmbeddings inserta/actualiza vectores por NodeID.
	UpsertEmbeddings(embs []Embedding) (int64, error)
	// AllEmbeddings devuelve todos los vectores guardados.
	AllEmbeddings() ([]Embedding, error)
}

// NodeHit es un resultado de búsqueda sobre el árbol: el nodo más su score BM25.
type NodeHit struct {
	Node
	Score float64
}

// SearchHit es un resultado de búsqueda: el mensaje más metadata del chat y el
// score BM25 (menor = más relevante). Snippet es la ventana de texto alrededor
// del match (FTS5 snippet()), para que el agente vea POR QUÉ matcheó sin tener
// que abrir el mensaje completo.
type SearchHit struct {
	Message
	ChatTitle  string
	ChatSource string
	Snippet    string
	Score      float64
}

// Config contiene la configuración para abrir un Store.
type Config struct {
	path    string
	verbose bool
}

// Option configura la apertura de un Store.
type Option func(*Config) error

// WithPath fija la ruta del archivo SQLite. Usar ":memory:" para tests.
func WithPath(path string) Option {
	return func(c *Config) error {
		if path == "" {
			return errors.New("path cannot be empty")
		}
		c.path = path
		return nil
	}
}

// WithVerbose habilita el logging de queries de GORM (debug).
func WithVerbose(verbose bool) Option {
	return func(c *Config) error {
		c.verbose = verbose
		return nil
	}
}

// store implementa Store sobre GORM.
type store struct {
	gdb *gorm.DB
}

// New abre (o crea) el Store en la ruta configurada y aplica la migración.
// Devuelve error si falta la ruta o si la conexión/migración fallan.
//
// Ejemplo:
//
//	s, err := db.New(db.WithPath("/home/me/.nem/nem.db"))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer s.Close()
func New(options ...Option) (Store, error) {
	cfg := &Config{}
	for _, option := range options {
		if err := option(cfg); err != nil {
			return nil, fmt.Errorf("failed to apply db option: %w", err)
		}
	}

	if cfg.path == "" {
		return nil, errors.New("path is required")
	}

	logLevel := logger.Silent
	if cfg.verbose {
		logLevel = logger.Info
	}

	gdb, err := gorm.Open(sqlite.Open(cfg.path), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database at %s: %w", cfg.path, err)
	}

	// Foreign keys no vienen activadas por defecto en SQLite.
	if err := gdb.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	s := &store{gdb: gdb}
	if err := s.Migrate(); err != nil {
		return nil, err
	}

	return s, nil
}

// Migrate aplica el esquema relacional y la capa FTS5.
func (s *store) Migrate() error {
	return migrate(s.gdb)
}

// Close cierra la conexión SQLite subyacente.
func (s *store) Close() error {
	sqlDB, err := s.gdb.DB()
	if err != nil {
		return fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}
	return sqlDB.Close()
}
