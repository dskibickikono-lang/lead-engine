package store

// The spend "day" is keyed in UTC (plain date('now')) to match the rest of the
// store, which timestamps with UTC datetime('now'). SpendToday and AddSpend must
// use the same clock so the daily cap sums and writes agree.

func (s *Store) SpendToday(api string) (float64, error) {
	var pln float64
	err := s.DB.QueryRow(`SELECT COALESCE(SUM(pln), 0) FROM spend_log
		WHERE api = ? AND day = date('now')`, api).Scan(&pln)
	return pln, err
}

func (s *Store) AddSpend(api string, pln float64) error {
	_, err := s.DB.Exec(`INSERT INTO spend_log (day, api, pln)
		VALUES (date('now'), ?, ?)`, api, pln)
	return err
}
