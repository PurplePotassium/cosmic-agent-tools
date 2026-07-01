package store

import (
	"database/sql"
	"errors"
)

// ErrNotFound is returned when a lookup by id/path misses.
var ErrNotFound = errors.New("not found")

// CreateProject inserts a project. If p.Created/Updated are zero they default to now.
func (s *Store) CreateProject(p *Project) error {
	if p.Created.IsZero() {
		p.Created = now()
	}
	p.Updated = now()
	if p.Status == "" {
		p.Status = StatusStopped
	}
	_, err := s.db.Exec(`INSERT INTO projects
		(id,name,repo_path,state_dir,branch,agent,model,status,preview,created,updated)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.RepoPath, p.StateDir, p.Branch, p.Agent, p.Model, p.Status, p.Preview,
		p.Created, p.Updated)
	return err
}

// UpdateProject writes the mutable fields (name/branch/agent/model/status/preview).
func (s *Store) UpdateProject(p *Project) error {
	p.Updated = now()
	res, err := s.db.Exec(`UPDATE projects SET
		name=?, branch=?, agent=?, model=?, status=?, preview=?, updated=? WHERE id=?`,
		p.Name, p.Branch, p.Agent, p.Model, p.Status, p.Preview, p.Updated, p.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetProjectStatus updates just the status column (called frequently by the loop).
func (s *Store) SetProjectStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE projects SET status=?, updated=? WHERE id=?`, status, now(), id)
	return err
}

// SetProjectAgent updates the live agent/model selection (re-read at each pass top).
func (s *Store) SetProjectAgent(id, agent, model string) error {
	_, err := s.db.Exec(`UPDATE projects SET agent=?, model=?, updated=? WHERE id=?`,
		agent, model, now(), id)
	return err
}

// DeleteProject removes a project and (via ON DELETE CASCADE) all its rows.
func (s *Store) DeleteProject(id string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}

func scanProject(row interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.RepoPath, &p.StateDir, &p.Branch, &p.Agent,
		&p.Model, &p.Status, &p.Preview, &p.Created, &p.Updated)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

const projectCols = `id,name,repo_path,state_dir,branch,agent,model,status,preview,created,updated`

// GetProject fetches one project by id.
func (s *Store) GetProject(id string) (*Project, error) {
	row := s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id=?`, id)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// GetProjectByRepoPath fetches a project by its repo working directory.
func (s *Store) GetProjectByRepoPath(repoPath string) (*Project, error) {
	row := s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE repo_path=?`, repoPath)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ListProjects returns all projects, newest first.
func (s *Store) ListProjects() ([]*Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY created DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
