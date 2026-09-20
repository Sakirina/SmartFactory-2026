package store

import (
	"context"
	"strings"

	"competition2026/product/platform/pkg/model"
)

// LatestValues returns one stored value per selected series independently of
// the trend window and its point limit. The caller applies resource grants.
func (s *Store) LatestValues(ctx context.Context, devices, keys []string) ([]model.Observation, error) {
	var query strings.Builder
	query.WriteString("SELECT data FROM latest WHERE 1=1")
	args := []any{}
	appendIn(&query, &args, "device_id", devices)
	appendIn(&query, &args, "key", keys)
	query.WriteString(" ORDER BY device_id,key LIMIT 2000")
	rows, err := s.DB.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := []model.Observation{}
	for rows.Next() {
		var raw string
		var point model.Observation
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = DecodeJSON([]byte(raw), &point); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}
