package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// ---------- Backlog snapshots (e4ef rollback) ----------

// BacklogSnapshot is one point-in-time capture of a project's
// backlog.md + completedlog.md just before a mutation.
type BacklogSnapshot struct {
	ID            int64     `json:"id"`
	ProjectID     string    `json:"project_id"`
	Ts            time.Time `json:"ts"`
	Reason        string    `json:"reason"`
	BacklogHash   string    `json:"backlog_hash"`
	BacklogBody   string    `json:"backlog_body"`
	CompletedHash string    `json:"completed_hash"`
	CompletedBody string    `json:"completed_body"`
}

// InsertBacklogSnapshot appends a snapshot. Idempotent-by-hash:
// if the previous snapshot for this project has the same hash pair,
// skips the write to avoid a snapshot per no-op sweep.
func (s *Store) InsertBacklogSnapshot(ctx context.Context, snap BacklogSnapshot) (int64, bool, error) {
	if snap.Ts.IsZero() {
		snap.Ts = time.Now().UTC()
	}
	snap.BacklogHash = hashHex(snap.BacklogBody)
	snap.CompletedHash = hashHex(snap.CompletedBody)

	var lastBacklog, lastCompleted string
	row := s.db.QueryRowContext(ctx, `
		SELECT backlog_hash, completed_hash
		  FROM backlog_snapshots
		 WHERE project_id = ?
		 ORDER BY id DESC
		 LIMIT 1`, snap.ProjectID)
	if err := row.Scan(&lastBacklog, &lastCompleted); err == nil {
		if lastBacklog == snap.BacklogHash && lastCompleted == snap.CompletedHash {
			return 0, false, nil // dedup
		}
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO backlog_snapshots(project_id, ts, reason, backlog_hash, backlog_body, completed_hash, completed_body)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		snap.ProjectID, snap.Ts.Format(time.RFC3339Nano), snap.Reason,
		snap.BacklogHash, snap.BacklogBody,
		snap.CompletedHash, snap.CompletedBody,
	)
	if err != nil {
		return 0, false, err
	}
	id, _ := res.LastInsertId()
	return id, true, nil
}

// ListBacklogSnapshots returns the N most recent snapshots for a project
// (newest first).
func (s *Store) ListBacklogSnapshots(ctx context.Context, projectID string, limit int) ([]BacklogSnapshot, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, ts, reason, backlog_hash, backlog_body, completed_hash, completed_body
		  FROM backlog_snapshots
		 WHERE project_id = ?
		 ORDER BY id DESC
		 LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BacklogSnapshot
	for rows.Next() {
		var sn BacklogSnapshot
		var tsStr string
		if err := rows.Scan(&sn.ID, &sn.ProjectID, &tsStr, &sn.Reason,
			&sn.BacklogHash, &sn.BacklogBody, &sn.CompletedHash, &sn.CompletedBody); err != nil {
			return nil, err
		}
		sn.Ts, _ = time.Parse(time.RFC3339Nano, tsStr)
		out = append(out, sn)
	}
	return out, rows.Err()
}

// GetBacklogSnapshot fetches one by id.
func (s *Store) GetBacklogSnapshot(ctx context.Context, id int64) (BacklogSnapshot, error) {
	var sn BacklogSnapshot
	var tsStr string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, ts, reason, backlog_hash, backlog_body, completed_hash, completed_body
		  FROM backlog_snapshots WHERE id = ?`, id).
		Scan(&sn.ID, &sn.ProjectID, &tsStr, &sn.Reason,
			&sn.BacklogHash, &sn.BacklogBody, &sn.CompletedHash, &sn.CompletedBody)
	if err != nil {
		return sn, err
	}
	sn.Ts, _ = time.Parse(time.RFC3339Nano, tsStr)
	return sn, nil
}

// PruneBacklogSnapshots keeps the most recent `keep` snapshots per
// project; drops the rest. Called periodically (or after mutations)
// to bound growth.
func (s *Store) PruneBacklogSnapshots(ctx context.Context, keep int) error {
	if keep <= 0 {
		keep = 100
	}
	// SQLite's DELETE-with-subquery form. Keeps things simple; at chief's
	// snapshot volumes this is fine.
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM backlog_snapshots
		 WHERE id NOT IN (
		       SELECT id FROM (
		           SELECT id, ROW_NUMBER() OVER (PARTITION BY project_id ORDER BY id DESC) AS rn
		             FROM backlog_snapshots
		       ) WHERE rn <= ?
		 )`, keep)
	return err
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8]) // 16 hex chars is plenty for dedup
}

// ---------- Task metadata (8fa3 estimator + future) ----------

// GetTaskMetadata returns the JSON object stored for a task, or empty map.
func (s *Store) GetTaskMetadata(ctx context.Context, taskID string) (map[string]any, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM task_metadata WHERE task_id = ?`, taskID).Scan(&raw)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return map[string]any{}, nil
		}
		return nil, err
	}
	m := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &m)
	return m, nil
}

// SetTaskMetadata merges the given keys into the task's metadata blob.
// Non-destructive: passing nil for a key doesn't delete it — set to null
// via the incoming map to erase.
func (s *Store) SetTaskMetadata(ctx context.Context, taskID string, patch map[string]any) error {
	cur, err := s.GetTaskMetadata(ctx, taskID)
	if err != nil {
		return err
	}
	for k, v := range patch {
		cur[k] = v
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO task_metadata(task_id, data) VALUES(?, ?)
		ON CONFLICT(task_id) DO UPDATE SET data = excluded.data`,
		taskID, string(b))
	return err
}

// ListTaskMetadata returns metadata for many tasks in one query.
func (s *Store) ListTaskMetadata(ctx context.Context, taskIDs []string) (map[string]map[string]any, error) {
	if len(taskIDs) == 0 {
		return map[string]map[string]any{}, nil
	}
	// Build placeholders. SQLite supports ~999 params — chief's task counts stay well under.
	placeholders := strings.Repeat(",?", len(taskIDs)-1)
	q := `SELECT task_id, data FROM task_metadata WHERE task_id IN (?` + placeholders + `)`
	args := make([]any, len(taskIDs))
	for i, id := range taskIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]any{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		m := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &m)
		out[id] = m
	}
	return out, rows.Err()
}

// ---------- Flag metadata (c302 triage) ----------

// GetFlagMetadata returns the JSON object stored for a flag, or empty map.
func (s *Store) GetFlagMetadata(ctx context.Context, flagID string) (map[string]any, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM flag_metadata WHERE flag_id = ?`, flagID).Scan(&raw)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return map[string]any{}, nil
		}
		return nil, err
	}
	m := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &m)
	return m, nil
}

// SetFlagMetadata merges a patch into the flag's metadata blob.
func (s *Store) SetFlagMetadata(ctx context.Context, flagID string, patch map[string]any) error {
	cur, err := s.GetFlagMetadata(ctx, flagID)
	if err != nil {
		return err
	}
	for k, v := range patch {
		cur[k] = v
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO flag_metadata(flag_id, data) VALUES(?, ?)
		ON CONFLICT(flag_id) DO UPDATE SET data = excluded.data`,
		flagID, string(b))
	return err
}

// ---------- Project time samples (3206) ----------

// TimeSample is one attributed slice of wall-clock spent on a project.
type TimeSample struct {
	ProjectID string    `json:"project_id"`
	Ts        time.Time `json:"ts"`
	Seconds   int64     `json:"seconds"`
	Source    string    `json:"source"`
}

// InsertTimeSample appends a time-sample row.
func (s *Store) InsertTimeSample(ctx context.Context, ts TimeSample) error {
	if ts.Ts.IsZero() {
		ts.Ts = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_time_samples(project_id, ts, seconds, source)
		VALUES(?, ?, ?, ?)`,
		ts.ProjectID, ts.Ts.Format(time.RFC3339Nano), ts.Seconds, ts.Source)
	return err
}

// SumTimeSamplesSince returns per-project total seconds since `since`.
func (s *Store) SumTimeSamplesSince(ctx context.Context, since time.Time) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, SUM(seconds) FROM project_time_samples
		 WHERE ts >= ? GROUP BY project_id`, since.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var pid string
		var secs int64
		if err := rows.Scan(&pid, &secs); err != nil {
			return nil, err
		}
		out[pid] = secs
	}
	return out, rows.Err()
}

// LastActivePerProject returns the most recent time-sample timestamp
// per project — for "worked on today" project.state.
func (s *Store) LastActivePerProject(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, MAX(ts) FROM project_time_samples GROUP BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var pid, tsStr string
		if err := rows.Scan(&pid, &tsStr); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			out[pid] = t
		}
	}
	return out, rows.Err()
}
