// Package cli define los comandos de nem sobre cobra.
package cli

import (
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version se inyecta en build time vía -ldflags (GoReleaser). Para instalaciones
// con `go install`, donde no hay ldflags, resolveVersion() cae al módulo.
var version = "dev"

// resolveVersion devuelve la versión inyectada, o la del módulo (build info)
// cuando se instaló con `go install ...@version`.
func resolveVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

// NewRootCmd construye el comando raíz `nem` con todos sus subcomandos.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "nem",
		Short:         "nem — your agent forgets, nem doesn't",
		Long:          "nem versions agent context the way git versions code.",
		Version:       resolveVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// --scope limita el acceso de lectura a un scope de ~/.nem/config.toml.
	// Vacío = acceso completo. También se puede fijar con NEM_SCOPE.
	root.PersistentFlags().String("scope", "", "limit read access to a scope from config.toml (or set NEM_SCOPE)")

	root.AddCommand(newInitCmd())
	root.AddCommand(newIngestCmd())
	root.AddCommand(newCloseCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newAddCmd())
	root.AddCommand(newCommitCmd())
	root.AddCommand(newLogCmd())
	root.AddCommand(newReadCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newRemoteCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newCloneCmd())
	root.AddCommand(newSkillCmd())
	root.AddCommand(newScopeCmd())
	root.AddCommand(newIndexCmd())
	root.AddCommand(newOutlineCmd())
	root.AddCommand(newTimelineCmd())
	root.AddCommand(newMCPCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newAnnotateCmd())
	root.AddCommand(newStatsCmd())
	root.AddCommand(newFactCmd())
	root.AddCommand(newTeamCmd())

	return root
}
