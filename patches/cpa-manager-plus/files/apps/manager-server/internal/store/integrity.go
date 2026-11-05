package store

import (
	"context"
	"fmt"
)

func (s *Store) CheckIntegrity(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		if value != "ok" {
			return fmt.Errorf("SQLite integrity: %s", value)
		}
	}
	return rows.Err()
}
