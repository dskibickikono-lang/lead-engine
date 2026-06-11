package store

import "database/sql"

func (s *Store) StartRun() (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO runs (started_at) VALUES (datetime('now'))`)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishRun(runID int64, status string) error {
	_, err := s.DB.Exec(`UPDATE runs SET finished_at=datetime('now'), status=? WHERE id=?`, status, runID)
	return err
}

func (s *Store) RecordStage(runID int64, stage, status, detail string) error {
	_, err := s.DB.Exec(`INSERT INTO run_stages (run_id, stage, status, detail, ended_at)
		VALUES (?,?,?,?,datetime('now'))
		ON CONFLICT(run_id, stage) DO UPDATE SET
		  status=excluded.status, detail=excluded.detail, ended_at=excluded.ended_at`,
		runID, stage, status, detail)
	return err
}

// LastFailedRun returns the most recent run — but only if it failed — plus
// the set of stages that completed ok in it. ok==false when the latest run
// succeeded or none exists: a success after a failure means there is
// nothing left to resume.
func (s *Store) LastFailedRun() (int64, map[string]bool, bool, error) {
	var runID int64
	err := s.DB.QueryRow(`SELECT id FROM runs WHERE status='failed'
		AND id = (SELECT MAX(id) FROM runs) LIMIT 1`).Scan(&runID)
	if err == sql.ErrNoRows {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, err
	}
	rows, err := s.DB.Query(`SELECT stage FROM run_stages WHERE run_id=? AND status='ok'`, runID)
	if err != nil {
		return 0, nil, false, err
	}
	defer rows.Close()
	done := map[string]bool{}
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			return 0, nil, false, err
		}
		done[st] = true
	}
	return runID, done, true, rows.Err()
}
