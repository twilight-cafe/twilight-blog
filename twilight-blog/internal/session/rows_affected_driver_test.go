package session

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
)

type rowsAffectedErrDriver struct{}

func (rowsAffectedErrDriver) Open(string) (driver.Conn, error) { return rowsAffectedErrConn{}, nil }

type rowsAffectedErrConn struct{}

func (rowsAffectedErrConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (rowsAffectedErrConn) Close() error              { return nil }
func (rowsAffectedErrConn) Begin() (driver.Tx, error) { return nil, errors.New("begin not supported") }
func (rowsAffectedErrConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return rowsAffectedErrResult{}, nil
}

type rowsAffectedErrResult struct{}

func (rowsAffectedErrResult) LastInsertId() (int64, error) {
	return 0, errors.New("last insert id not supported")
}
func (rowsAffectedErrResult) RowsAffected() (int64, error) {
	return 0, errors.New("rows affected boom")
}

var (
	registerRowsAffectedDriverOnce sync.Once
	rowsAffectedDriverName         string
)

func openRowsAffectedErrDB(t *testing.T) *sql.DB {
	t.Helper()
	registerRowsAffectedDriverOnce.Do(func() {
		rowsAffectedDriverName = "session-rows-affected-error"
		sql.Register(rowsAffectedDriverName, rowsAffectedErrDriver{})
	})
	db, err := sql.Open(rowsAffectedDriverName, "")
	if err != nil {
		t.Fatalf("open fake database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRevokeLegacySessionsReportsRowsAffectedError(t *testing.T) {
	if _, err := NewStore(openRowsAffectedErrDB(t)).RevokeLegacySessions(context.Background()); err == nil {
		t.Fatal("RevokeLegacySessions must report a RowsAffected error")
	}
}
