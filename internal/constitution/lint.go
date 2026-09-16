// Package constitution scans a project's Claude Code session logs for
// tool-use patterns that violate a project-specific rule set defined
// in .chief/lint.yaml.
//
// Design v1: periodic sweep only. A proper post-turn Stop hook that
// pushes activity into chiefd in real time is deferred until Claude
// hooks are wired (M2). The periodic sweep runs every N hours,
// checks jsonl entries modified since the last sweep, and raises an
// info-urgency flag per hit. Chief already has claudetrace.ListSessions
// so we don't need any new file-discovery machinery.
//
// Rule format (.chief/lint.yaml):
//
//	disable: false           # skip this project entirely
//	sweep_hours: 6           # override default sweep cadence
//	forbidden:
//	  - regex: "\\bcurl\\b"
//	    reason: "no direct HTTP; use internal/http.Client"
//	  - regex: "npm install --unsafe-perm"
//	    reason: "no --unsafe-perm; use standard install"
//
// Each forbidden entry is applied to every Bash tool_use command in
// the scanned session entries.
package constitution

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Rules is the parsed .chief/lint.yaml.
type Rules struct {
	Disable     bool           `yaml:"disable,omitempty"`
	SweepHours  int            `yaml:"sweep_hours,omitempty"`
	Forbidden   []ForbiddenRule `yaml:"forbidden,omitempty"`
}

// ForbiddenRule is one pattern + explanation.
type ForbiddenRule struct {
	Regex  string `yaml:"regex"`
	Reason string `yaml:"reason"`
}

// LoadRules reads .chief/lint.yaml under projectPath. Returns
// (Rules{}, false, nil) if the file doesn't exist — that's the
// "no linting configured" case, not an error.
func LoadRules(projectPath string) (Rules, bool, error) {
	path := filepath.Join(projectPath, ".chief", "lint.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Rules{}, false, nil
		}
		return Rules{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var r Rules
	if err := yaml.Unmarshal(b, &r); err != nil {
		return Rules{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return r, true, nil
}

// Violation is one hit: a Bash command that matches a forbidden rule.
type Violation struct {
	SessionID string    // Claude session UUID
	Ts        time.Time // when the tool_use event fired
	Command   string    // the Bash input (clipped)
	Pattern   string    // the regex that matched
	Reason    string    // human-readable rule text
}

// Compiled is a compiled rule set — call Compile once per sweep,
// then reuse across many Scan calls.
type Compiled struct {
	Patterns []compiledPattern
}

type compiledPattern struct {
	Re     *regexp.Regexp
	Reason string
}

// Compile compiles every rule's regex. Invalid regexes are skipped
// with a warning-shaped error so one bad pattern doesn't break the
// whole ruleset. The returned Compiled is safe to reuse across calls.
func Compile(r Rules) (Compiled, []error) {
	var c Compiled
	var errs []error
	for _, f := range r.Forbidden {
		re, err := regexp.Compile(f.Regex)
		if err != nil {
			errs = append(errs, fmt.Errorf("bad regex %q: %w", f.Regex, err))
			continue
		}
		c.Patterns = append(c.Patterns, compiledPattern{Re: re, Reason: f.Reason})
	}
	return c, errs
}

// ScanSessions reads every jsonl entry from the given session files
// with an mtime >= since, extracts Bash tool_use commands, and returns
// violations of the compiled rules. Skips files whose mtime is older
// than `since` entirely (avoids reading old sessions on every sweep).
func ScanSessions(sessionPaths []string, since time.Time, c Compiled) ([]Violation, error) {
	if len(c.Patterns) == 0 {
		return nil, nil
	}
	var out []Violation
	for _, p := range sessionPaths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if info.ModTime().Before(since) {
			continue
		}
		vs, err := scanOneFile(p, since, c)
		if err != nil {
			// Log-and-continue: one corrupt session doesn't block sweep.
			continue
		}
		out = append(out, vs...)
	}
	return out, nil
}

func scanOneFile(path string, since time.Time, c Compiled) ([]Violation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<22) // long tool_use inputs are common
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	var out []Violation

	for sc.Scan() {
		cmd, ts, ok := extractBashCommand(sc.Bytes())
		if !ok {
			continue
		}
		if !ts.IsZero() && ts.Before(since) {
			continue
		}
		for _, pat := range c.Patterns {
			if pat.Re.MatchString(cmd) {
				clip := cmd
				if len(clip) > 200 {
					clip = clip[:197] + "…"
				}
				out = append(out, Violation{
					SessionID: sessionID,
					Ts:        ts,
					Command:   clip,
					Pattern:   pat.Re.String(),
					Reason:    pat.Reason,
				})
			}
		}
	}
	return out, sc.Err()
}

// extractBashCommand parses one jsonl entry looking for a Bash
// tool_use event; returns the command text and timestamp on hit.
// Claude Code entries have shape:
//
//	{"type":"assistant","message":{"content":[
//	  {"type":"tool_use","name":"Bash","input":{"command":"...","description":"..."}}
//	]},"timestamp":"..."}
//
// The parser is defensive — Claude's log shape has evolved and may
// differ slightly across versions.
func extractBashCommand(line []byte) (string, time.Time, bool) {
	var entry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Content []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Input struct {
					Command string `json:"command"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return "", time.Time{}, false
	}
	if entry.Type != "assistant" {
		return "", time.Time{}, false
	}
	for _, c := range entry.Message.Content {
		if c.Type == "tool_use" && c.Name == "Bash" && c.Input.Command != "" {
			var ts time.Time
			if entry.Timestamp != "" {
				if t, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
					ts = t
				} else if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
					ts = t
				}
			}
			return c.Input.Command, ts, true
		}
	}
	return "", time.Time{}, false
}
