package factory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
)

type store struct {
	db   *sql.DB
	lock *os.File
}
type record struct {
	kind, id string
	value    any
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another Factory daemon owns %s: %w", dir, err)
	}
	s := &store{lock: lock}
	db, err := sql.Open("sqlite", filepath.Join(dir, "factory.sqlite"))
	if err != nil {
		s.close()
		return nil, err
	}
	s.db = db
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS records (kind TEXT NOT NULL, id TEXT NOT NULL, document BLOB NOT NULL, PRIMARY KEY (kind,id));`); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}
func (s *store) close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		err = errors.Join(err, syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN), s.lock.Close())
	}
	return err
}
func (s *store) save(records ...record) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range records {
		data, err := json.Marshal(r.value)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO records(kind,id,document) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET document=excluded.document`, r.kind, r.id, data); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func loadRecords[T any](s *store, kind string) (map[string]T, error) {
	rows, err := s.db.Query(`SELECT id,document FROM records WHERE kind=? ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]T{}
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		var value T
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, fmt.Errorf("corrupt %s %s: %w", kind, id, err)
		}
		values[id] = value
	}
	return values, rows.Err()
}
