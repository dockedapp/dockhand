package operations

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	maxHistoryPerOperation = 50
	schema                 = `
CREATE TABLE IF NOT EXISTS operation_history (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	operation_name TEXT    NOT NULL,
	started_at     INTEGER NOT NULL,
	finished_at    INTEGER,
	exit_code      INTEGER,
	output         TEXT
);
CREATE INDEX IF NOT EXISTS idx_op_name ON operation_history(operation_name);
`
)

// HistoryRecord is a single operation run stored in the database.
type HistoryRecord struct {
	ID            int64      `json:"id"`
	OperationName string     `json:"operationName"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	ExitCode      *int       `json:"exitCode,omitempty"`
	Output        string     `json:"output"`
}

// DB wraps the SQLite connection for operation history.
type DB struct {
	db *sql.DB
}

// OpenDB opens (or creates) the history database at dataDir/history.db.
func OpenDB(dataDir string) (*DB, error) {
	path := filepath.Join(dataDir, "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open history db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &DB{db: db}, nil
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// InsertRun records the start of an operation run and returns its ID.
func (d *DB) InsertRun(name string, startedAt time.Time) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO operation_history (operation_name, started_at) VALUES (?, ?)`,
		name, startedAt.Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateRun finalises a run with its exit code and captured output.
func (d *DB) UpdateRun(id int64, finishedAt time.Time, exitCode int, output string) error {
	_, err := d.db.Exec(
		`UPDATE operation_history SET finished_at=?, exit_code=?, output=? WHERE id=?`,
		finishedAt.Unix(), exitCode, output, id,
	)
	return err
}

// ListHistory returns up to limit records for the named operation, newest first.
func (d *DB) ListHistory(name string, limit int) ([]HistoryRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.db.Query(
		`SELECT id, operation_name, started_at, finished_at, exit_code, output
		   FROM operation_history
		  WHERE operation_name = ?
		  ORDER BY started_at DESC
		  LIMIT ?`,
		name, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []HistoryRecord
	for rows.Next() {
		var r HistoryRecord
		var startedUnix int64
		var finishedUnix sql.NullInt64
		var exitCode sql.NullInt64

		if err := rows.Scan(&r.ID, &r.OperationName, &startedUnix,
			&finishedUnix, &exitCode, &r.Output); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(startedUnix, 0)
		if finishedUnix.Valid {
			t := time.Unix(finishedUnix.Int64, 0)
			r.FinishedAt = &t
		}
		if exitCode.Valid {
			c := int(exitCode.Int64)
			r.ExitCode = &c
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// ListAllHistory returns up to limit records across all operations, newest first.
func (d *DB) ListAllHistory(limit int) ([]HistoryRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.Query(
		`SELECT id, operation_name, started_at, finished_at, exit_code, output
		   FROM operation_history
		  ORDER BY started_at DESC
		  LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []HistoryRecord
	for rows.Next() {
		var r HistoryRecord
		var startedUnix int64
		var finishedUnix sql.NullInt64
		var exitCode sql.NullInt64

		if err := rows.Scan(&r.ID, &r.OperationName, &startedUnix,
			&finishedUnix, &exitCode, &r.Output); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(startedUnix, 0)
		if finishedUnix.Valid {
			t := time.Unix(finishedUnix.Int64, 0)
			r.FinishedAt = &t
		}
		if exitCode.Valid {
			c := int(exitCode.Int64)
			r.ExitCode = &c
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// ListAllAppHistory returns up to limit records for app operations (operation_name
// contains a colon, i.e. "appName:opName"), newest first.
func (d *DB) ListAllAppHistory(limit int) ([]HistoryRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.Query(
		`SELECT id, operation_name, started_at, finished_at, exit_code, output
		   FROM operation_history
		  WHERE operation_name LIKE '%:%'
		  ORDER BY started_at DESC
		  LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []HistoryRecord
	for rows.Next() {
		var r HistoryRecord
		var startedUnix int64
		var finishedUnix sql.NullInt64
		var exitCode sql.NullInt64

		if err := rows.Scan(&r.ID, &r.OperationName, &startedUnix,
			&finishedUnix, &exitCode, &r.Output); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(startedUnix, 0)
		if finishedUnix.Valid {
			t := time.Unix(finishedUnix.Int64, 0)
			r.FinishedAt = &t
		}
		if exitCode.Valid {
			c := int(exitCode.Int64)
			r.ExitCode = &c
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// Prune removes old history records, keeping the most recent maxHistoryPerOperation per operation.
func (d *DB) Prune(name string) error {
	_, err := d.db.Exec(`
		DELETE FROM operation_history
		WHERE operation_name = ?
		  AND id NOT IN (
			SELECT id FROM operation_history
			WHERE operation_name = ?
			ORDER BY started_at DESC
			LIMIT ?
		  )`, name, name, maxHistoryPerOperation)
	return err
}
