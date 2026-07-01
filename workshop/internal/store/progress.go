package store

import (
	"database/sql"
	"errors"
)

// GetProgress returns the agent's latest self-report for a project (zero value if none).
func (s *Store) GetProgress(projectID string) (Progress, error) {
	var p Progress
	row := s.db.QueryRow(
		`SELECT phase,task,plan,note,result,updated FROM progress WHERE project_id=?`, projectID)
	err := row.Scan(&p.Phase, &p.Task, &p.Plan, &p.Note, &p.Result, &p.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Progress{}, nil
	}
	return p, err
}

// SetProgress upserts the agent's self-report for a project.
func (s *Store) SetProgress(projectID string, p Progress) error {
	_, err := s.db.Exec(
		`INSERT INTO progress (project_id,phase,task,plan,note,result,updated)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(project_id) DO UPDATE SET
		   phase=excluded.phase, task=excluded.task, plan=excluded.plan,
		   note=excluded.note, result=excluded.result, updated=excluded.updated`,
		projectID, p.Phase, p.Task, p.Plan, p.Note, p.Result, p.Updated)
	return err
}
