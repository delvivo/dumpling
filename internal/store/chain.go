package store

import (
	"errors"
	"fmt"
)

// ChainError names the first link of a chain that is missing or not ok.
type ChainError struct {
	Link   string // backup id
	Status string // "missing", "corrupted", "deleted"
}

func (e *ChainError) Error() string { return fmt.Sprintf("link %s is %s", e.Link, e.Status) }

// ChainHead returns the newest non-deleted chain backup of a plan's database, whatever its status:
// a corrupted head must close the chain rather than let an older link fork it.
func (s *Store) ChainHead(planID, database string) (Backup, bool, error) {
	b, err := s.scanBackup(s.DB.QueryRow(`SELECT `+backupCols+` FROM backups
		WHERE plan_id=? AND database=? AND chain_id<>'' AND status<>'deleted'
		ORDER BY started_at DESC, id DESC LIMIT 1`, planID, database))
	if errors.Is(err, ErrNotFound) {
		return Backup{}, false, nil
	}
	return b, err == nil, err
}

// Ancestors walks parent_id from b to the chain's full and returns those links oldest first
// (b itself excluded). A missing or non-ok link yields a *ChainError naming it.
func (s *Store) Ancestors(b Backup) ([]Backup, error) {
	var out []Backup
	for id := b.ParentID; id != ""; {
		p, err := s.Backup(id)
		if errors.Is(err, ErrNotFound) {
			return nil, &ChainError{Link: id, Status: "missing"}
		}
		if err != nil {
			return nil, err
		}
		if p.Status != "ok" {
			return nil, &ChainError{Link: id, Status: p.Status}
		}
		out = append([]Backup{p}, out...)
		id = p.ParentID
		if len(out) > 10000 { // ponytail: cycle guard; parent ids are only ever set to older rows
			return nil, &ChainError{Link: id, Status: "cyclic"}
		}
	}
	return out, nil
}

// Dependents returns every non-deleted backup that (transitively) applies on top of id.
func (s *Store) Dependents(id string) ([]Backup, error) {
	var out []Backup
	queue := []string{id}
	for len(queue) > 0 {
		rows, err := s.DB.Query(`SELECT `+backupCols+` FROM backups WHERE parent_id=? AND status<>'deleted'`, queue[0])
		if err != nil {
			return nil, err
		}
		queue = queue[1:]
		for rows.Next() {
			b, err := s.scanBackup(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, b)
			queue = append(queue, b.ID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
