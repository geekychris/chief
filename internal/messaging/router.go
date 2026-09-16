package messaging

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Rules is a resolved routing configuration for one project (or the
// global default). Each urgency maps to a list of backend NAMES; the
// router dispatches to all of them (fan-out). Empty PerUrgency +
// non-empty Default means every urgency uses Default. Disable=true
// short-circuits the whole route (opts a project out of side channels).
type Rules struct {
	Default    []string
	PerUrgency map[Urgency][]string
	Disable    bool
}

// Backends returns the list of backend names to fan out to for the
// given urgency. PerUrgency wins over Default when set; unknown
// urgencies fall back to Default. Never returns nil (empty slice
// signals "nothing configured").
func (r Rules) Backends(u Urgency) []string {
	if r.Disable {
		return nil
	}
	if r.PerUrgency != nil {
		if names, ok := r.PerUrgency[u]; ok {
			return names
		}
	}
	return r.Default
}

// Router owns the backend registry and knows how to fan a Message out
// to zero-or-more configured backends per the resolved Rules.
type Router struct {
	// Backends maps backend name → implementation. Populated once at
	// chiefd boot; never mutated at runtime.
	Backends map[string]Backend

	// GlobalRules is the fallback used when no per-project override
	// applies (or when PerProjectRules is nil).
	GlobalRules Rules

	// PerProjectRules, when non-nil, is called per Dispatch to resolve
	// the effective rules for a project. Return an empty Rules{} to
	// inherit GlobalRules. Return Rules{Disable: true} to opt a project
	// out entirely.
	PerProjectRules func(projectID string) Rules

	// SendTimeout bounds each per-backend Send call. Zero = 10s.
	// Individual backends can implement their own tighter timeouts but
	// this is the hard ceiling.
	SendTimeout time.Duration

	mu sync.Mutex // reserved for future runtime backend swaps
}

// Register attaches a backend to the router. Overwrites any prior
// registration for the same name — chiefd boots by resetting the
// registry to whatever config says.
func (r *Router) Register(b Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Backends == nil {
		r.Backends = map[string]Backend{}
	}
	r.Backends[b.Name()] = b
}

// Dispatch fans the message out to every backend configured for its
// urgency + project. Errors are logged and returned in a slice; the
// call never fails hard so a Slack outage doesn't wedge flag.raise.
// Callers can inspect the returned slice for per-backend errors.
func (r *Router) Dispatch(ctx context.Context, m Message) []DispatchResult {
	var rules Rules
	if r.PerProjectRules != nil {
		rules = r.PerProjectRules(m.ProjectID)
	}
	if len(rules.Backends(m.Urgency)) == 0 && !rules.Disable {
		rules = r.GlobalRules
	}
	names := rules.Backends(m.Urgency)
	if len(names) == 0 {
		return nil
	}
	timeout := r.SendTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	results := make([]DispatchResult, 0, len(names))
	for _, name := range names {
		b, ok := r.Backends[name]
		if !ok {
			slog.Warn("messaging: unknown backend name in rules; skipping", "name", name)
			results = append(results, DispatchResult{Backend: name, Skipped: "unknown"})
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		err := b.Send(sctx, m)
		cancel()
		results = append(results, DispatchResult{
			Backend:  name,
			Duration: time.Since(start),
			Error:    err,
		})
		if err != nil {
			slog.Warn("messaging: backend send failed", "backend", name, "err", err, "urgency", m.Urgency)
		}
	}
	return results
}

// DispatchResult is one per-backend outcome. Skipped is non-empty when
// the router bailed before calling Send (e.g., "unknown backend name").
type DispatchResult struct {
	Backend  string
	Duration time.Duration
	Error    error
	Skipped  string
}
