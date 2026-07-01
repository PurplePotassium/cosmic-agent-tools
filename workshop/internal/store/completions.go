package store

import "time"

// ListCompletions returns a project's completions, newest first, capped at limit.
func (s *Store) ListCompletions(projectID string, limit int) ([]Completion, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id,title,result,completed FROM completions WHERE project_id=?
		 ORDER BY completed DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Completion{}
	for rows.Next() {
		var c Completion
		if err := rows.Scan(&c.ID, &c.Title, &c.Result, &c.Completed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddCompletion inserts one completion record (generating id/timestamp if absent).
func (s *Store) AddCompletion(projectID string, c Completion) error {
	if c.ID == "" {
		c.ID = "ws-" + time.Now().UTC().Format("20060102150405.000000")
	}
	if c.Completed == "" {
		c.Completed = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO completions (id,project_id,title,result,completed) VALUES (?,?,?,?,?)`,
		c.ID, projectID, c.Title, c.Result, c.Completed)
	return err
}

// CompletionIDs returns the current set of completion ids (reconcile snapshot).
func (s *Store) CompletionIDs(projectID string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT id FROM completions WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = true
	}
	return set, rows.Err()
}
