package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/geekychris/chief/internal/attention"
	"github.com/geekychris/chief/internal/store"
)

const (
	// DefaultDedupInterval — daily is more than enough; dup titles across
	// projects don't appear that fast, and cheap Jaccard on token sets
	// runs in ~ms even for hundreds of tasks.
	DefaultDedupInterval = 24 * time.Hour
	// DefaultDedupThreshold — Jaccard threshold above which titles are
	// "likely duplicates". 0.55 catches near-restatements ("update auth
	// middleware" vs "update the auth middleware") without triggering on
	// generic verbs ("fix bug", "add tests").
	DefaultDedupThreshold = 0.55
)

// stopWords keeps single-token overlap from producing false positives
// ("add" or "fix" alone shouldn't collapse two different tasks).
var stopWords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "for": {}, "and": {}, "or": {}, "of": {},
	"in": {}, "on": {}, "to": {}, "with": {}, "add": {}, "fix": {}, "update": {},
	"support": {}, "chief": {}, "task": {},
}

// DedupSweeper (9b5f) walks every pending task across every project and
// flags high-Jaccard-similarity pairs so obvious duplicates surface as
// info flags with a diff-recipe.
type DedupSweeper struct {
	Store     *store.Store
	Attention *attention.Manager
	Interval  time.Duration
	Threshold float64
	Clock     func() time.Time

	mu       sync.Mutex
	lastSeen map[string]time.Time // "pairKey" → last flag ts
}

// pairKey normalizes an unordered task-id pair so we don't re-flag it
// twice per sweep or across ticks.
func pairKey(a, b string) string {
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}

func (d *DedupSweeper) Run(ctx context.Context) {
	interval := d.Interval
	if interval <= 0 {
		interval = DefaultDedupInterval
	}
	select {
	case <-time.After(5 * time.Minute):
	case <-ctx.Done():
		return
	}
	if err := d.Sweep(ctx); err != nil {
		slog.Warn("dedup sweeper: first sweep failed", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.Sweep(ctx); err != nil {
				slog.Warn("dedup sweeper: tick failed", "err", err)
			}
		}
	}
}

// Sweep computes similar-title pairs and raises one info flag per pair.
// Only pairs where the two tasks belong to DIFFERENT projects are
// considered — same-project duplicates are the user's own decision to
// re-express.
func (d *DedupSweeper) Sweep(ctx context.Context) error {
	if d.Store == nil || d.Attention == nil {
		return fmt.Errorf("dedup sweeper: missing dependency")
	}
	thr := d.Threshold
	if thr <= 0 {
		thr = DefaultDedupThreshold
	}
	projs, err := d.Store.ListProjects(ctx)
	if err != nil {
		return err
	}
	nameByID := map[string]string{}
	type entry struct {
		task    store.Task
		project store.Project
		tokens  map[string]struct{}
	}
	var all []entry
	for _, p := range projs {
		nameByID[p.ID] = p.Name
		ts, err := d.Store.ListTasks(ctx, store.TaskFilter{ProjectID: p.ID, Status: store.TaskPending})
		if err != nil {
			continue
		}
		for _, t := range ts {
			all = append(all, entry{task: t, project: p, tokens: tokenize(t.Title)})
		}
	}
	// O(n²) but chief's pending task count stays in the hundreds; this is fine.
	type hit struct {
		a, b entry
		sim  float64
	}
	var hits []hit
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[i].project.ID == all[j].project.ID {
				continue
			}
			sim := jaccard(all[i].tokens, all[j].tokens)
			if sim >= thr {
				hits = append(hits, hit{a: all[i], b: all[j], sim: sim})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].sim > hits[j].sim })

	raised := 0
	for _, h := range hits {
		key := pairKey(h.a.task.ID, h.b.task.ID)
		if last, ok := d.lastFor(key); ok && time.Since(last) < 7*24*time.Hour {
			continue
		}
		body := fmt.Sprintf(
			"Possible duplicate (Jaccard %.2f):\n  %s / {id:%s} — %s\n  %s / {id:%s} — %s\nCompare:\n  chief backlog list --project %s\n  chief backlog list --project %s",
			h.sim,
			h.a.project.Name, h.a.task.ID, h.a.task.Title,
			h.b.project.Name, h.b.task.ID, h.b.task.Title,
			h.a.project.Name, h.b.project.Name,
		)
		// Raise on the project of the FIRST task; the second is referenced
		// inline. Alternative: raise on both — but that doubles the flag
		// count for no extra info.
		_, err := d.Attention.Raise(ctx, attention.RaiseOpts{
			ProjectID: h.a.project.ID,
			Kind:      store.FlagKindQuestion,
			Urgency:   store.UrgencyInfo,
			Question:  body,
		})
		if err != nil {
			continue
		}
		d.recordFor(key, d.now())
		raised++
	}
	if raised > 0 {
		slog.Info("dedup sweeper: raised", "hits", raised, "total_candidates", len(hits))
	}
	return nil
}

// tokenize lowercases, strips punctuation, drops stopwords + 1-char tokens.
func tokenize(s string) map[string]struct{} {
	out := map[string]struct{}{}
	var b strings.Builder
	flush := func() {
		if b.Len() < 2 {
			b.Reset()
			return
		}
		w := b.String()
		if _, isStop := stopWords[w]; !isStop {
			out[w] = struct{}{}
		}
		b.Reset()
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func (d *DedupSweeper) lastFor(key string) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.lastSeen[key]
	return t, ok
}

func (d *DedupSweeper) recordFor(key string, t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastSeen == nil {
		d.lastSeen = map[string]time.Time{}
	}
	d.lastSeen[key] = t
}

func (d *DedupSweeper) now() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now().UTC()
}
