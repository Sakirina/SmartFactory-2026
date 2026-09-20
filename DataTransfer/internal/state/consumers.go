package state

import "context"

func (s *Store) SaveConsumer(ctx context.Context, id string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO telemetry_consumers(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data WHERE telemetry_consumers.data<>excluded.data`, id, data)
	return err
}
func (s *Store) Consumers(ctx context.Context) (map[string][]byte, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,data FROM telemetry_consumers")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]byte{}
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		result[id] = data
	}
	return result, rows.Err()
}
