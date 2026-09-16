package attention

import (
	"strconv"
	"strings"
	"time"
)

// DNDWindow is a resolved DND schedule ("22:00"–"07:00" on Mon–Fri).
// Both global config and per-project override materialize into this
// shape via the Resolve helpers in the chiefd wiring layer.
type DNDWindow struct {
	Start    string // "HH:MM" 24h local
	End      string // "HH:MM"
	Weekdays []int  // 0=Sun..6=Sat; empty=every day
	Disable  bool   // true: never in DND (project override that opts out)
}

// InWindow returns true iff `now` falls inside this DND schedule. Zero-
// value DNDWindow (no start/end) is treated as "no DND" → returns false.
func (w DNDWindow) InWindow(now time.Time) bool {
	if w.Disable {
		return false
	}
	if w.Start == "" || w.End == "" {
		return false
	}
	if len(w.Weekdays) > 0 {
		wd := int(now.Weekday())
		ok := false
		for _, d := range w.Weekdays {
			if d == wd {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	sm, sok := parseHM(w.Start)
	em, eok := parseHM(w.End)
	if !sok || !eok {
		return false
	}
	nm := now.Hour()*60 + now.Minute()
	if sm == em {
		return false
	}
	if sm < em {
		return nm >= sm && nm < em
	}
	// Wraps midnight: e.g. 22:00→07:00 → window is [22:00, 24:00) ∪ [00:00, 07:00).
	return nm >= sm || nm < em
}

// parseHM parses "HH:MM" into total minutes from midnight. Returns
// (minutes, true) on success.
func parseHM(s string) (int, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
