package store

import "time"

// StartRun opens a new run row for a project and returns its id.
func (s *Store) StartRun(projectID string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO runs (project_id,started,status) VALUES (?,?, 'running')`,
		projectID, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EndRun closes a run with a final status and iteration count.
func (s *Store) EndRun(runID int64, iterations int, status string) error {
	t := now()
	_, err := s.db.Exec(`UPDATE runs SET ended=?, iterations=?, status=? WHERE id=?`,
		t, iterations, status, runID)
	return err
}

// AddIteration inserts an iteration row (started now) and returns its id.
func (s *Store) AddIteration(it Iteration) (int64, error) {
	if it.Started.IsZero() {
		it.Started = now()
	}
	res, err := s.db.Exec(`INSERT INTO iterations
		(project_id,run_id,num,agent,model,spice,log_path,commit_sha,files_changed,status,started,ended)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		it.ProjectID, it.RunID, it.Num, it.Agent, it.Model, it.Spice, it.LogPath,
		it.CommitSHA, it.FilesChanged, it.Status, it.Started, it.Ended)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishIteration records the end state of an iteration.
func (s *Store) FinishIteration(id int64, commitSHA, filesChanged, status string) error {
	t := now()
	_, err := s.db.Exec(
		`UPDATE iterations SET commit_sha=?, files_changed=?, status=?, ended=? WHERE id=?`,
		commitSHA, filesChanged, status, t, id)
	return err
}

// ListIterations returns recent iterations for a project, newest first.
func (s *Store) ListIterations(projectID string, limit int) ([]Iteration, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT
		id,project_id,run_id,num,agent,model,spice,log_path,commit_sha,files_changed,status,started,ended
		FROM iterations WHERE project_id=? ORDER BY id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Iteration{}
	for rows.Next() {
		var it Iteration
		var ended *time.Time
		if err := rows.Scan(&it.ID, &it.ProjectID, &it.RunID, &it.Num, &it.Agent, &it.Model,
			&it.Spice, &it.LogPath, &it.CommitSHA, &it.FilesChanged, &it.Status,
			&it.Started, &ended); err != nil {
			return nil, err
		}
		it.Ended = ended
		out = append(out, it)
	}
	return out, rows.Err()
}

// RunningIteration returns the in-flight iteration for a project (status='running'),
// or nil. Used to compute pass elapsed time and the model currently running.
func (s *Store) RunningIteration(projectID string) (*Iteration, error) {
	row := s.db.QueryRow(`SELECT
		id,project_id,run_id,num,agent,model,spice,log_path,commit_sha,files_changed,status,started,ended
		FROM iterations WHERE project_id=? AND status='running' ORDER BY id DESC LIMIT 1`, projectID)
	var it Iteration
	var ended *time.Time
	err := row.Scan(&it.ID, &it.ProjectID, &it.RunID, &it.Num, &it.Agent, &it.Model,
		&it.Spice, &it.LogPath, &it.CommitSHA, &it.FilesChanged, &it.Status, &it.Started, &ended)
	if err != nil {
		return nil, err
	}
	it.Ended = ended
	return &it, nil
}

// LastPassIteration returns the highest-numbered completed iteration (for status).
func (s *Store) LastPassIteration(projectID string) (*Iteration, error) {
	row := s.db.QueryRow(`SELECT
		id,project_id,run_id,num,agent,model,spice,log_path,commit_sha,files_changed,status,started,ended
		FROM iterations WHERE project_id=? AND status='ok' ORDER BY id DESC LIMIT 1`, projectID)
	var it Iteration
	var ended *time.Time
	err := row.Scan(&it.ID, &it.ProjectID, &it.RunID, &it.Num, &it.Agent, &it.Model,
		&it.Spice, &it.LogPath, &it.CommitSHA, &it.FilesChanged, &it.Status, &it.Started, &ended)
	if err != nil {
		return nil, err
	}
	it.Ended = ended
	return &it, nil
}
