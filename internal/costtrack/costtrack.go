// Package costtrack reads Claude Code session jsonl files and
// produces token + dollar summaries per project + per day.
//
// Each assistant message in the jsonl carries a `message.usage`
// block with input_tokens, output_tokens, cache_creation_input_tokens,
// and cache_read_input_tokens fields, plus a `message.model` label.
// This package sums those fields per model and applies a per-model
// pricing table to derive dollar costs.
//
// Pricing values here reflect Anthropic's public list prices for the
// Claude 4.x family as of the code's writing. Users can override via
// config.yaml — Chief passes the resolved table to Compute so this
// package doesn't need to import config (avoids an import cycle).
package costtrack

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ModelPricing is the per-million-token USD price for one model.
// Cached-read is Anthropic's "cache read hit" price (much cheaper);
// cache-creation is the "cache write" price (slightly more than
// input). Chief applies the appropriate rate to each usage bucket.
type ModelPricing struct {
	InputPerM         float64
	OutputPerM        float64
	CacheCreationPerM float64
	CacheReadPerM     float64
}

// DefaultPricing is the fallback table used when no config override
// exists. Numbers are Anthropic's published USD/million-tokens rates
// for the Claude 4.x family. Update as prices change or override via
// config.yaml under `messaging.pricing` (reserved for later — for now
// callers can Compute with their own table).
var DefaultPricing = map[string]ModelPricing{
	// Opus family
	"claude-opus-4-5":  {InputPerM: 15, OutputPerM: 75, CacheCreationPerM: 18.75, CacheReadPerM: 1.50},
	"claude-opus-4-6":  {InputPerM: 15, OutputPerM: 75, CacheCreationPerM: 18.75, CacheReadPerM: 1.50},
	"claude-opus-4-7":  {InputPerM: 15, OutputPerM: 75, CacheCreationPerM: 18.75, CacheReadPerM: 1.50},
	// Sonnet family
	"claude-sonnet-4-5": {InputPerM: 3, OutputPerM: 15, CacheCreationPerM: 3.75, CacheReadPerM: 0.30},
	"claude-sonnet-4-6": {InputPerM: 3, OutputPerM: 15, CacheCreationPerM: 3.75, CacheReadPerM: 0.30},
	// Haiku family
	"claude-haiku-4-5":  {InputPerM: 1, OutputPerM: 5, CacheCreationPerM: 1.25, CacheReadPerM: 0.10},
}

// Usage sums the four token buckets and derived cost for a slice of
// entries. Returned by Compute; also stored per-project + per-day in
// the aggregated views.
type Usage struct {
	Model              string    `json:"model,omitempty"`
	InputTokens        int64     `json:"input_tokens"`
	OutputTokens       int64     `json:"output_tokens"`
	CacheCreation      int64     `json:"cache_creation_tokens"`
	CacheRead          int64     `json:"cache_read_tokens"`
	CostUSD            float64   `json:"cost_usd"`
	Messages           int       `json:"messages"`
	FirstTs            time.Time `json:"first_ts,omitempty"`
	LastTs             time.Time `json:"last_ts,omitempty"`
}

// Add sums two usages. When both have models, the receiver's wins
// (used to fold per-model into a total).
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheCreation += o.CacheCreation
	u.CacheRead += o.CacheRead
	u.CostUSD += o.CostUSD
	u.Messages += o.Messages
	if u.FirstTs.IsZero() || (!o.FirstTs.IsZero() && o.FirstTs.Before(u.FirstTs)) {
		u.FirstTs = o.FirstTs
	}
	if !o.LastTs.IsZero() && o.LastTs.After(u.LastTs) {
		u.LastTs = o.LastTs
	}
}

// ProjectReport groups aggregated usage for one project across all
// its session files.
type ProjectReport struct {
	ProjectPath string             `json:"project_path"`
	Total       Usage              `json:"total"`
	PerModel    map[string]Usage   `json:"per_model"`
	PerDay      map[string]Usage   `json:"per_day"` // YYYY-MM-DD
}

// Compute scans one project's Claude session jsonl files, extracts
// usage from every assistant message, applies pricing, and returns
// an aggregated ProjectReport.
//
// sessionPaths are typically what claudetrace.ListSessions returns
// for the project. pricing is the resolved rates table (nil uses
// DefaultPricing). since filters out entries older than the cutoff
// (zero = include everything).
func Compute(sessionPaths []string, pricing map[string]ModelPricing, since time.Time) (ProjectReport, error) {
	if pricing == nil {
		pricing = DefaultPricing
	}
	report := ProjectReport{
		PerModel: map[string]Usage{},
		PerDay:   map[string]Usage{},
	}
	for _, p := range sessionPaths {
		if err := computeFile(p, pricing, since, &report); err != nil {
			// Skip corrupt files rather than blocking the whole
			// project — costtrack is best-effort observability.
			continue
		}
	}
	return report, nil
}

func computeFile(path string, pricing map[string]ModelPricing, since time.Time, r *ProjectReport) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<22)
	for sc.Scan() {
		u, ok := extractUsage(sc.Bytes())
		if !ok {
			continue
		}
		if !since.IsZero() && !u.LastTs.IsZero() && u.LastTs.Before(since) {
			continue
		}
		u.CostUSD = priceUsage(u, pricing)
		u.Messages = 1
		// Fold into totals.
		r.Total.Add(u)
		perModel := r.PerModel[u.Model]
		perModel.Model = u.Model
		perModel.Add(u)
		r.PerModel[u.Model] = perModel
		day := u.LastTs.Format("2006-01-02")
		perDay := r.PerDay[day]
		perDay.Add(u)
		r.PerDay[day] = perDay
	}
	// Best-effort — corrupt lines caught by extractUsage returning ok=false.
	_ = sc.Err()
	return nil
}

// extractUsage pulls (model, usage, ts) out of one jsonl line. The
// Claude Code session format wraps this under
// entry.message.usage.{input_tokens, output_tokens, ...} and
// entry.message.model, with entry.timestamp at the top level.
func extractUsage(line []byte) (Usage, bool) {
	var entry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return Usage{}, false
	}
	if entry.Type != "assistant" || entry.Message.Model == "" {
		return Usage{}, false
	}
	u := Usage{
		Model:         entry.Message.Model,
		InputTokens:   entry.Message.Usage.InputTokens,
		OutputTokens:  entry.Message.Usage.OutputTokens,
		CacheCreation: entry.Message.Usage.CacheCreationInputTokens,
		CacheRead:     entry.Message.Usage.CacheReadInputTokens,
	}
	if entry.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			u.FirstTs = t
			u.LastTs = t
		} else if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
			u.FirstTs = t
			u.LastTs = t
		}
	}
	return u, true
}

// priceUsage applies the pricing table to one usage entry. Models
// not in the table are matched loosely — "claude-sonnet-4-6-fast"
// falls back to "claude-sonnet-4-6"'s rates by stripping the
// trailing suffix. This copes with -latest, -fast, and dated
// aliases without a full alias table.
func priceUsage(u Usage, pricing map[string]ModelPricing) float64 {
	p, ok := pricing[u.Model]
	if !ok {
		// Try progressive suffix strip so "claude-sonnet-4-6-20260901"
		// falls back to "claude-sonnet-4-6"'s rates.
		key := u.Model
		for {
			idx := strings.LastIndex(key, "-")
			if idx < 0 {
				break
			}
			key = key[:idx]
			if fallback, found := pricing[key]; found {
				p = fallback
				ok = true
				break
			}
		}
		if !ok {
			return 0
		}
	}
	return float64(u.InputTokens)/1_000_000*p.InputPerM +
		float64(u.OutputTokens)/1_000_000*p.OutputPerM +
		float64(u.CacheCreation)/1_000_000*p.CacheCreationPerM +
		float64(u.CacheRead)/1_000_000*p.CacheReadPerM
}

// SessionsForPath is a helper that walks ~/.claude/projects/<slug>
// for a given project dir and returns the jsonl file paths. Chief
// callers already do this via claudetrace.ListSessions; kept here
// for tests + standalone use.
func SessionsForPath(projectPath string) ([]string, error) {
	// Copy of claudetrace.SlugForPath logic to avoid a mutual import.
	slug := slugFor(projectPath)
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".claude", "projects", slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out, nil
}

// slugFor mirrors Claude Code's project-dir mangling: '/' and '_'
// both replaced with '-', leading dash preserved (paths already start
// with '/'). Matches internal/claudetrace.SlugForPath — kept as a
// private copy here to avoid a claudetrace→costtrack import cycle.
func slugFor(cwd string) string {
	cleaned := filepath.Clean(cwd)
	s := strings.NewReplacer("/", "-", "_", "-").Replace(cleaned)
	if !strings.HasPrefix(s, "-") {
		s = "-" + s
	}
	return s
}
