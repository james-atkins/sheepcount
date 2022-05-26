package main

import (
	"context"
	"database/sql"
	"time"
)

func DeleteExpired(ctx context.Context, deleteSince time.Duration, db *sql.DB) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(
		ctx,
		"UPDATE users SET identifier = NULL WHERE identifier IS NOT NULL AND last_seen + ? < CAST(strftime('%s','now') AS INTEGER)",
		deleteSince.Seconds(),
	)
	if err != nil {
		return 0, err
	}

	err = tx.Commit()
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}
