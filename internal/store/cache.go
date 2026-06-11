package store

import (
	"database/sql"
	"errors"
	"time"
)

func (s *Store) CacheGet(api, identifier string, ttl time.Duration) ([]byte, bool, error) {
	var payload, fetched string
	err := s.DB.QueryRow(`SELECT payload, fetched_at FROM api_cache
		WHERE api = ? AND identifier = ?`, api, identifier).Scan(&payload, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	t, err := time.Parse(time.RFC3339, fetched)
	if err != nil || time.Since(t) > ttl {
		return nil, false, nil
	}
	return []byte(payload), true, nil
}

func (s *Store) CachePut(api, identifier string, payload []byte) error {
	_, err := s.DB.Exec(`INSERT INTO api_cache (api, identifier, payload, fetched_at)
		VALUES (?,?,?,?)
		ON CONFLICT(api, identifier) DO UPDATE SET
		  payload = excluded.payload, fetched_at = excluded.fetched_at`,
		api, identifier, string(payload), time.Now().UTC().Format(time.RFC3339))
	return err
}
