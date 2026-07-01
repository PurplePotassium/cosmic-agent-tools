package store

import "time"

// ListBacklog returns a project's backlog, drained order (TOP first = lowest position).
func (s *Store) ListBacklog(projectID string) ([]BacklogItem, error) {
	rows, err := s.db.Query(
		`SELECT id,title,detail,created,position FROM backlog WHERE project_id=? ORDER BY position ASC`,
		projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BacklogItem{}
	for rows.Next() {
		var b BacklogItem
		var pos float64
		if err := rows.Scan(&b.ID, &b.Title, &b.Detail, &b.Created, &pos); err != nil {
			return nil, err
		}
		b.Position = int(pos)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) backlogEdgePos(projectID string, top bool) float64 {
	var pos float64
	if top {
		_ = s.db.QueryRow(`SELECT COALESCE(MIN(position),1) FROM backlog WHERE project_id=?`, projectID).Scan(&pos)
		return pos - 1
	}
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(position),-1) FROM backlog WHERE project_id=?`, projectID).Scan(&pos)
	return pos + 1
}

// AddBacklog inserts one item at the top or bottom. If item.ID/Created are empty
// they are generated. Returns the stored item.
func (s *Store) AddBacklog(projectID string, item BacklogItem, top bool) (BacklogItem, error) {
	if item.ID == "" {
		item.ID = "ws-" + time.Now().UTC().Format("20060102150405.000000")
	}
	if item.Created == "" {
		item.Created = time.Now().UTC().Format(time.RFC3339)
	}
	pos := s.backlogEdgePos(projectID, top)
	item.Position = int(pos)
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO backlog (id,project_id,title,detail,position,created) VALUES (?,?,?,?,?,?)`,
		item.ID, projectID, item.Title, item.Detail, pos, item.Created)
	return item, err
}

// DeleteBacklogItem removes one item by id.
func (s *Store) DeleteBacklogItem(projectID, id string) error {
	_, err := s.db.Exec(`DELETE FROM backlog WHERE project_id=? AND id=?`, projectID, id)
	return err
}

// DeleteBacklogItems removes many items by id (used by pass-end reconcile for the
// item(s) the agent drained).
func (s *Store) DeleteBacklogItems(projectID string, ids []string) error {
	for _, id := range ids {
		if _, err := s.db.Exec(`DELETE FROM backlog WHERE project_id=? AND id=?`, projectID, id); err != nil {
			return err
		}
	}
	return nil
}

// AppendBacklogItems inserts items at the bottom, skipping ids that already exist
// (used by reconcile for follow-up tasks the agent added mid-pass).
func (s *Store) AppendBacklogItems(projectID string, items []BacklogItem) error {
	for _, it := range items {
		var exists int
		_ = s.db.QueryRow(`SELECT COUNT(1) FROM backlog WHERE project_id=? AND id=?`, projectID, it.ID).Scan(&exists)
		if exists > 0 {
			continue
		}
		if _, err := s.AddBacklog(projectID, it, false); err != nil {
			return err
		}
	}
	return nil
}

// ReorderBacklog assigns positions 0..n-1 in the given id order. Ids not present
// are ignored; items omitted from the list keep a position after the listed ones.
func (s *Store) ReorderBacklog(projectID string, orderedIDs []string) error {
	for i, id := range orderedIDs {
		if _, err := s.db.Exec(`UPDATE backlog SET position=? WHERE project_id=? AND id=?`,
			float64(i), projectID, id); err != nil {
			return err
		}
	}
	return nil
}

// BacklogIDs returns the current set of backlog ids (reconcile snapshot).
func (s *Store) BacklogIDs(projectID string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT id FROM backlog WHERE project_id=?`, projectID)
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
