package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/output"
	"github.com/spf13/cobra"
)

// newCommitCmd crea `nem commit -m`: congela los mensajes staged en un commit
// inmutable (copia el texto en un snapshot).
func newCommitCmd() *cobra.Command {
	var (
		message  string
		chatFlag string
		team     string
	)
	cmd := &cobra.Command{
		Use:   "commit",
		Short: "Create an immutable commit from the staged messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommit(cmd, chatFlag, message, team)
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "", "commit message (required)")
	cmd.Flags().StringVar(&chatFlag, "chat", "", "chat id (default: detected session)")
	cmd.Flags().StringVar(&team, "team", "", "commit into a team store instead of personal")
	_ = cmd.MarkFlagRequired("message")
	cmd.AddCommand(newCommitPromoteCmd())
	return cmd
}

func runCommit(cmd *cobra.Command, chatFlag, message, team string) error {
	personal, err := openStore()
	if err != nil {
		return err
	}
	defer personal.Close()

	chatID, _, err := resolveActiveChat(chatFlag)
	if err != nil {
		return err
	}
	if chatID == "" {
		return errors.New("no active session detected; use --chat <id>")
	}

	staged, err := personal.StagedMessages(chatID)
	if err != nil {
		return err
	}
	if len(staged) == 0 {
		return errors.New("nothing staged; run 'nem add -L <n>' first")
	}

	// El destino: el store personal, o un team store. El staging siempre vive en
	// el personal (es donde corre la sesión del agente).
	dst := personal
	if team != "" {
		ts, err := openStoreFor(team)
		if err != nil {
			return err
		}
		defer ts.Close()
		dst = ts
		// El team store necesita el chat y los mensajes para poder leer y buscar
		// este commit como si hubiera llegado por sync.
		if err := copyChatAndMessages(personal, dst, chatID, staged); err != nil {
			return err
		}
	}

	commit, err := commitInto(dst, chatID, message, config.UserName(), staged)
	if err != nil {
		return err
	}
	if err := personal.ClearStaging(chatID); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "commit %s: %q (%d messages) → %s\n",
		shortHash(commit.Hash), message, len(staged), originTag(team))
	if team != "" {
		fmt.Fprintf(out, "run 'nem team sync %s' to publish it\n", team)
	}
	return nil
}

// commitInto congela los mensajes dados en un commit inmutable dentro de store y
// lo devuelve. Es la pieza compartida por el commit personal y el de equipo.
func commitInto(store db.Store, chatID, message, author string, staged []db.Message) (*db.Commit, error) {
	snapshot, err := output.BuildSnapshot(staged)
	if err != nil {
		return nil, err
	}
	commit := &db.Commit{
		Hash:      commitHash(chatID, message, snapshot),
		ChatID:    chatID,
		Branch:    "main",
		Message:   message,
		Author:    author,
		MsgFrom:   staged[0].ID,
		MsgTo:     staged[len(staged)-1].ID,
		Snapshot:  snapshot,
		CreatedAt: time.Now().Unix(),
	}
	if err := store.CreateCommit(commit); err != nil {
		return nil, err
	}
	return commit, nil
}

// copyChatAndMessages replica el chat y sus mensajes de src a dst (idempotente),
// para que un commit escrito en un team store sea legible y buscable ahí.
func copyChatAndMessages(src, dst db.Store, chatID string, msgs []db.Message) error {
	chat, err := src.GetChat(chatID)
	if err != nil {
		return err
	}
	if chat != nil {
		if err := dst.UpsertChat(chat); err != nil {
			return err
		}
	}
	if _, err := dst.InsertMessages(msgs); err != nil {
		return err
	}
	return nil
}

// newCommitPromoteCmd crea `nem commit promote <hash> --team <name>`: copia un
// commit personal existente a un team store (el flujo curado: trabajás en privado
// y promovés lo bueno). Idempotente gracias al hash determinista.
func newCommitPromoteCmd() *cobra.Command {
	var team string
	cmd := &cobra.Command{
		Use:   "promote <hash>",
		Short: "Copy an existing personal commit into a team store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommitPromote(cmd, args[0], team)
		},
	}
	cmd.Flags().StringVar(&team, "team", "", "team store to promote into (required)")
	_ = cmd.MarkFlagRequired("team")
	return cmd
}

func runCommitPromote(cmd *cobra.Command, hash, team string) error {
	personal, err := openStore()
	if err != nil {
		return err
	}
	defer personal.Close()

	commit, err := personal.GetCommit(hash)
	if err != nil {
		return err
	}
	if commit == nil {
		return fmt.Errorf("commit %q not found in the personal store", hash)
	}

	ts, err := openStoreFor(team)
	if err != nil {
		return err
	}
	defer ts.Close()

	out := cmd.OutOrStdout()
	// Idempotencia: si el team ya tiene este commit (mismo hash), no se duplica.
	if existing, err := ts.GetCommit(commit.Hash); err != nil {
		return err
	} else if existing != nil {
		fmt.Fprintf(out, "commit %s already in team %q\n", shortHash(commit.Hash), team)
		return nil
	}

	// Reconstruye los mensajes desde el snapshot inmutable para que el team store
	// pueda buscar el contenido, no solo leer el commit.
	msgs, err := messagesFromSnapshot(commit)
	if err != nil {
		return err
	}
	if chat, err := personal.GetChat(commit.ChatID); err != nil {
		return err
	} else if chat != nil {
		if err := ts.UpsertChat(chat); err != nil {
			return err
		}
	}
	if _, err := ts.InsertMessages(msgs); err != nil {
		return err
	}

	copyCommit := *commit
	if len(msgs) > 0 {
		copyCommit.MsgFrom = msgs[0].ID
		copyCommit.MsgTo = msgs[len(msgs)-1].ID
	}
	if err := ts.CreateCommit(&copyCommit); err != nil {
		return err
	}
	fmt.Fprintf(out, "promoted commit %s to team %q\nrun 'nem team sync %s' to publish it\n",
		shortHash(commit.Hash), team, team)
	return nil
}

// messagesFromSnapshot reconstruye filas de mensaje desde el snapshot inmutable de
// un commit, con ids deterministas (chatID:seq).
func messagesFromSnapshot(commit *db.Commit) ([]db.Message, error) {
	snap, err := output.ParseSnapshot(commit.Snapshot)
	if err != nil {
		return nil, err
	}
	msgs := make([]db.Message, 0, len(snap))
	for _, m := range snap {
		msgs = append(msgs, db.Message{
			ID:        fmt.Sprintf("%s:%d", commit.ChatID, m.Seq),
			ChatID:    commit.ChatID,
			Role:      m.Role,
			Content:   m.Content,
			Timestamp: m.Timestamp,
			Seq:       m.Seq,
		})
	}
	return msgs, nil
}

// commitHash deriva un hash estable del contenido del commit (chat + mensaje +
// snapshot). Es determinista por contenido: habilita dedup real entre personas y
// que promover el mismo commit dos veces sea idempotente.
func commitHash(chatID, message, snapshot string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s", chatID, message, snapshot)
	return hex.EncodeToString(h.Sum(nil))
}
