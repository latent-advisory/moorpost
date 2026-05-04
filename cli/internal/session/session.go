// Package session ties Provider+Agent+Sync together for one Moorpost project.
//
// CLI commands construct a Session at startup from the project's config and
// then call methods on it. The Session never imports concrete impls; it only
// knows the three interfaces.
package session

import (
	"github.com/latent-advisory/moorpost/cli/internal/agent"
	"github.com/latent-advisory/moorpost/cli/internal/provider"
	"github.com/latent-advisory/moorpost/cli/internal/sync"
)

// Session bundles a project's configured cloud provider, AI agent, and sync
// engine. Construct with New; never instantiate the struct directly so
// future fields (logger, clock, etc.) can be added without breaking callers.
type Session struct {
	Provider provider.Provider
	Agent    agent.Agent
	Sync     sync.Sync

	// ProjectSlug is the lower-cased, hyphenated identifier used in tmux
	// session names, mutagen labels, and the ~/moorpost/<slug>/ dir.
	ProjectSlug string

	// ProjectAbsDir is the absolute path of the project on the local machine,
	// used for state-path encoding (e.g., agent.SessionStatePath).
	ProjectAbsDir string
}

// New constructs a Session from already-built interface values. The caller
// owns the lifetime of all three.
func New(p provider.Provider, a agent.Agent, s sync.Sync, projectSlug, projectAbsDir string) *Session {
	return &Session{
		Provider:      p,
		Agent:         a,
		Sync:          s,
		ProjectSlug:   projectSlug,
		ProjectAbsDir: projectAbsDir,
	}
}
