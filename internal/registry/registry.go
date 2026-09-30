// Package registry keeps the live model directory and resolves the many names
// a client may use for the same upstream model.
//
// Three identifiers describe one model in the AStudio world:
//
//	slug    xopglm52           upstream inference "model" field
//	name    GLM-5.2            what the desktop app shows
//	id      lm_glm52           stable directory id (bot/models/configs)
//
// Clients are accepted with any of them (case-insensitively), plus any alias an
// operator configures.
package registry

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"astudio2api/internal/astron"
)

// Model is one callable model.
type Model struct {
	Slug             string   `json:"slug"`
	Name             string   `json:"name"`
	DirectoryID      string   `json:"directory_id,omitempty"`
	Provider         string   `json:"provider,omitempty"`
	Description      string   `json:"description,omitempty"`
	ContextWindow    int      `json:"context_window,omitempty"`
	InputModalities  []string `json:"input_modalities,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoning string   `json:"default_reasoning_effort,omitempty"`
	PointMultiplier  string   `json:"point_multiplier,omitempty"`
	Priority         int      `json:"priority,omitempty"`
	Available        bool     `json:"available"`
	Source           string   `json:"source,omitempty"`
}

// Registry holds the resolved model set.
type Registry struct {
	mu          sync.RWMutex
	models      []*Model
	alias       map[string]*Model // lowercased key -> model
	aliases     map[string]string // operator overrides
	refreshedAt time.Time
	lastErr     string
	source      string
}

// New creates an empty registry.
func New() *Registry {
	return &Registry{alias: map[string]*Model{}, aliases: map[string]string{}}
}

// SetAliases installs operator-configured alias overrides (alias -> target).
func (r *Registry) SetAliases(aliases map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aliases = map[string]string{}
	for k, v := range aliases {
		r.aliases[normalize(k)] = v
	}
	r.rebuildAliasesLocked()
}

// Replace installs a freshly fetched model set.
func (r *Registry) Replace(models []*Model, source string, lastErr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models = models
	r.source = source
	r.lastErr = lastErr
	r.refreshedAt = time.Now()
	r.rebuildAliasesLocked()
}

func (r *Registry) rebuildAliasesLocked() {
	alias := map[string]*Model{}
	for _, m := range r.models {
		for _, key := range []string{m.Slug, m.Name, m.DirectoryID} {
			if key == "" {
				continue
			}
			if _, exists := alias[normalize(key)]; !exists {
				alias[normalize(key)] = m
			}
		}
	}
	for from, to := range r.aliases {
		if target, ok := alias[normalize(to)]; ok {
			alias[from] = target
		}
	}
	r.alias = alias
}

// Resolve maps any client-supplied model name to a known model.
// It returns nil when the name is unknown.
func (r *Registry) Resolve(name string) *Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.alias[normalize(name)]; ok {
		return m
	}
	// Fall back to a permissive match so an unknown-but-plausible upstream slug
	// still reaches the gateway instead of failing locally.
	return nil
}

// SlugFor returns the upstream slug to send for a client-supplied model name.
// Unknown names are passed through unchanged so the upstream can decide.
func (r *Registry) SlugFor(name string) string {
	if m := r.Resolve(name); m != nil {
		return m.Slug
	}
	return name
}

// List returns all models, default-first then by name.
func (r *Registry) List() []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Model, len(r.models))
	copy(out, r.models)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Status describes the freshness of the directory.
func (r *Registry) Status() (count int, source string, refreshedAt time.Time, lastErr string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.models), r.source, r.refreshedAt, r.lastErr
}

// Sync refreshes the directory from the upstream. It first tries the agent
// catalog (richest metadata) and falls back to the account model configuration.
func (r *Registry) Sync(ctx context.Context, client *astron.Client, session *astron.Session, bearer string) error {
	var (
		models  []*Model
		sources []string
		errs    []string
	)

	// The agent catalog carries the richest metadata (context window, reasoning
	// levels); the account configuration is the authoritative list of models the
	// credential may actually call. Merge both so nothing callable is hidden.
	if bearer != "" {
		if catalog, err := client.FetchCatalog(ctx, bearer); err == nil && len(catalog) > 0 {
			for _, c := range catalog {
				models = append(models, fromCatalog(c))
			}
			sources = append(sources, "model-manager")
		} else if err != nil {
			errs = append(errs, err.Error())
		}
	}

	if session != nil && session.Valid() {
		if configs, _, err := client.FetchModelCredentials(ctx, session); err == nil && len(configs) > 0 {
			seen := make(map[string]bool, len(models))
			for _, m := range models {
				seen[normalize(m.Slug)] = true
			}
			added := 0
			for _, c := range configs {
				m := fromConfig(c)
				if seen[normalize(m.Slug)] {
					continue
				}
				seen[normalize(m.Slug)] = true
				// Rank directory-only entries after the agent catalog so the models
				// the desktop app actually offers stay at the top.
				m.Priority = 1000 + len(models)
				models = append(models, m)
				added++
			}
			if added > 0 || len(sources) == 0 {
				sources = append(sources, "bot/models/configs")
			}
		} else if err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(models) == 0 {
		return &SyncError{Messages: errs}
	}
	r.Replace(models, strings.Join(sources, "+"), strings.Join(errs, "; "))
	return nil
}

// SyncError reports why a directory refresh produced nothing.
type SyncError struct{ Messages []string }

func (e *SyncError) Error() string {
	if len(e.Messages) == 0 {
		return "model directory refresh produced no models"
	}
	return "model directory refresh failed: " + strings.Join(e.Messages, "; ")
}

func fromCatalog(c astron.CatalogModel) *Model {
	m := &Model{
		Slug:             c.Slug,
		Name:             c.DisplayName,
		Description:      c.Description,
		ContextWindow:    c.ContextWindow,
		InputModalities:  c.InputModalities,
		DefaultReasoning: c.DefaultReasoning,
		Priority:         c.Priority,
		Available:        c.SupportedInAPI,
		Source:           "model-manager",
	}
	if m.Name == "" {
		m.Name = c.Slug
	}
	for _, r := range c.ReasoningLevels {
		if r.Effort != "" {
			m.ReasoningEfforts = append(m.ReasoningEfforts, r.Effort)
		}
	}
	return m
}

func fromConfig(c astron.ModelConfig) *Model {
	m := &Model{
		Slug:        c.Model,
		Name:        c.Name,
		DirectoryID: c.ID,
		Provider:    c.Provider,
		Available:   true,
		Source:      "bot/models/configs",
	}
	if m.Slug == "" {
		m.Slug = c.ID
	}
	if m.Name == "" {
		m.Name = m.Slug
	}
	switch v := c.PointMultiplier.(type) {
	case string:
		m.PointMultiplier = v
	case float64:
		m.PointMultiplier = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return m
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
