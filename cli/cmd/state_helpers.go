package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/latent-advisory/moorpost/cli/internal/state"
)

// vmName returns a stable per-project VM name. v1: "<slug>-vm".
func vmName(c *Context) string {
	if c == nil || c.Config == nil {
		return ""
	}
	return c.Config.ProjectSlug + "-vm"
}

// projectKey returns the absolute project directory used as the state.Projects
// map key. Falls back to the slug if no project dir is set.
func projectKey(c *Context) string {
	if c == nil {
		return ""
	}
	if c.ProjectDir != "" {
		abs, err := absDir(c.ProjectDir)
		if err == nil {
			return abs
		}
	}
	if c.Config != nil {
		return c.Config.ProjectSlug
	}
	return ""
}

// withProjectState loads state under a file lock, runs fn against the
// project's record (zero-valued if missing), persists, and returns.
func withProjectState(c *Context, fn func(ps *state.ProjectState) error) error {
	if c == nil {
		return errors.New("context is nil")
	}
	if c.StatePath == "" {
		return errors.New("context has no StatePath")
	}
	key := projectKey(c)
	if key == "" {
		return errors.New("cannot derive project key (missing config?)")
	}
	return state.WithLock(c.StatePath, func(s *state.State) error {
		ps := s.Projects[key]
		if ps.Slug == "" && c.Config != nil {
			ps.Slug = c.Config.ProjectSlug
		}
		if err := fn(&ps); err != nil {
			return err
		}
		s.SetProject(key, ps)
		// Cache pointer back onto Context so subsequent reads see fresh values.
		c.State = s
		return nil
	})
}

// withVM is the same as withProjectState but for state.VMs[vmID]. fn may
// update the record; the caller is responsible for setting the project's
// VMID separately if it changes.
func withVM(c *Context, vmID string, fn func(*state.VMRecord) error) error {
	if vmID == "" {
		return errors.New("withVM: vmID required")
	}
	return state.WithLock(c.StatePath, func(s *state.State) error {
		if s.VMs == nil {
			s.VMs = map[string]state.VMRecord{}
		}
		rec := s.VMs[vmID]
		if err := fn(&rec); err != nil {
			return err
		}
		s.VMs[vmID] = rec
		c.State = s
		return nil
	})
}

func absDir(p string) (string, error) {
	abs, err := absolute(p)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		// abs of a non-existent path is fine — we just want the canonical form.
		return abs, nil
	}
	return abs, nil
}

// absolute is a tiny wrapper kept here to make absDir testable without
// importing filepath in every helper.
func absolute(p string) (string, error) {
	return filepathAbs(p)
}

// filepathAbs is a var so tests could swap it; we just delegate.
var filepathAbs = func(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path required")
	}
	if p[0] == '/' {
		return p, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return cwd + "/" + p, nil
}
