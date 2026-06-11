package store

func (s *Store) SpendToday(api string) (float64, error) {
	var pln float64
	err := s.DB.QueryRow(`SELECT COALESCE(SUM(pln), 0) FROM spend_log
		WHERE api = ? AND day = date('now','localtime')`, api).Scan(&pln)
	return pln, err
}

func (s *Store) AddSpend(api string, pln float64) error {
	_, err := s.DB.Exec(`INSERT INTO spend_log (day, api, pln)
		VALUES (date('now','localtime'), ?, ?)`, api, pln)
	return err
}
