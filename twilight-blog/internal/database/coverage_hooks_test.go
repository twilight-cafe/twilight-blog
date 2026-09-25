package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRow struct {
	scan func(dest ...any) error
}

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

func setInt(dest any, value int) error {
	if ptr, ok := dest.(*int); ok {
		*ptr = value
	}
	return nil
}

func TestOpenReportsHookFailures(t *testing.T) {
	t.Run("sql.Open", func(t *testing.T) {
		original := sqlOpen
		sqlOpen = func(string, string) (*sql.DB, error) { return nil, errors.New("sql.Open failed") }
		t.Cleanup(func() { sqlOpen = original })

		if _, err := Open(filepath.Join(t.TempDir(), "open.db")); err == nil {
			t.Fatal("Open must report a sql.Open error")
		}
	})

	t.Run("migrate", func(t *testing.T) {
		original := migrateFn
		migrateFn = func(*sql.DB) error { return errors.New("migration failed") }
		t.Cleanup(func() { migrateFn = original })

		if _, err := Open(filepath.Join(t.TempDir(), "migrate.db")); err == nil {
			t.Fatal("Open must report a migration error")
		}
	})

	t.Run("verify pragmas", func(t *testing.T) {
		original := verifyPragmasFn
		verifyPragmasFn = func(*sql.DB) error { return errors.New("pragma failed") }
		t.Cleanup(func() { verifyPragmasFn = original })

		if _, err := Open(filepath.Join(t.TempDir(), "pragma.db")); err == nil {
			t.Fatal("Open must report a pragma verification error")
		}
	})
}

func TestSnapshotReportsOpenAndChmodFailures(t *testing.T) {
	t.Run("sql.Open", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.db")
		if err := writeEmptyFile(src); err != nil {
			t.Fatalf("prepare source: %v", err)
		}
		original := sqlOpen
		sqlOpen = func(string, string) (*sql.DB, error) { return nil, errors.New("sql.Open failed") }
		t.Cleanup(func() { sqlOpen = original })

		if err := Snapshot(src, filepath.Join(t.TempDir(), "dst.db")); err == nil {
			t.Fatal("Snapshot must report a sql.Open error")
		}
	})

	t.Run("chmod", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.db")
		db, err := Open(src)
		if err != nil {
			t.Fatalf("create source database: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close source database: %v", err)
		}
		dst := filepath.Join(t.TempDir(), "dst.db")

		original := chmodPath
		chmodPath = func(string, os.FileMode) error { return errors.New("chmod failed") }
		t.Cleanup(func() { chmodPath = original })

		if err := Snapshot(src, dst); err != nil {
			t.Fatalf("Snapshot chmod failure must be reported as a warning only, not an error: %v", err)
		}
	})
}

func TestRestrictDatabasePermissionsWarnsOnChmodFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blog.db")
	if err := writeEmptyFile(path); err != nil {
		t.Fatalf("prepare database file: %v", err)
	}

	original := chmodPath
	chmodPath = func(string, os.FileMode) error { return errors.New("chmod failed") }
	t.Cleanup(func() { chmodPath = original })

	restrictDatabasePermissions(path)
}

func TestBuildDSNCoversWindowsAndExistingQuerySeparator(t *testing.T) {
	original := isWindows
	isWindows = true
	got := buildDSN(`C:\data\blog.db?mode=rw`)
	isWindows = original
	if !strings.Contains(got, `C:/data/blog.db`) {
		t.Fatalf("windows DSN = %q, want backslashes rewritten", got)
	}
	if !strings.Contains(got, "&_pragma=") {
		t.Fatalf("windows DSN with existing query = %q, want & separator", got)
	}

	got = buildDSN("file:blog.db?mode=rw")
	if !strings.Contains(got, "&_pragma=") {
		t.Fatalf("existing-query DSN = %q, want & separator", got)
	}
}

func TestVerifyPragmasAllFailureBranches(t *testing.T) {
	original := pragmaRow
	t.Cleanup(func() { pragmaRow = original })

	t.Run("foreign_keys scan", func(t *testing.T) {
		pragmaRow = func(*sql.DB, string) rowScanner {
			return fakeRow{scan: func(...any) error { return errors.New("scan failed") }}
		}
		if err := verifyPragmas(nil); err == nil {
			t.Fatal("verifyPragmas must report a foreign_keys scan error")
		}
	})

	t.Run("foreign_keys disabled", func(t *testing.T) {
		pragmaRow = func(*sql.DB, string) rowScanner {
			return fakeRow{scan: func(dest ...any) error { return setInt(dest[0], 0) }}
		}
		if err := verifyPragmas(nil); err == nil {
			t.Fatal("verifyPragmas must reject foreign_keys=0")
		}
	})

	t.Run("busy_timeout scan", func(t *testing.T) {
		calls := 0
		pragmaRow = func(*sql.DB, string) rowScanner {
			return fakeRow{scan: func(dest ...any) error {
				calls++
				if calls == 1 {
					return setInt(dest[0], 1)
				}
				return errors.New("scan failed")
			}}
		}
		if err := verifyPragmas(nil); err == nil {
			t.Fatal("verifyPragmas must report a busy_timeout scan error")
		}
	})

	t.Run("busy_timeout disabled", func(t *testing.T) {
		calls := 0
		pragmaRow = func(*sql.DB, string) rowScanner {
			return fakeRow{scan: func(dest ...any) error {
				calls++
				if calls == 1 {
					return setInt(dest[0], 1)
				}
				return setInt(dest[0], 0)
			}}
		}
		if err := verifyPragmas(nil); err == nil {
			t.Fatal("verifyPragmas must reject busy_timeout=0")
		}
	})
}

func TestPruneExpiredErrorBranches(t *testing.T) {
	t.Run("exec", func(t *testing.T) {
		db, err := sql.Open("sqlite", buildDSN(filepath.Join(t.TempDir(), "closed.db")))
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
		if _, err := PruneExpired(context.Background(), db); err == nil {
			t.Fatal("PruneExpired must report an Exec error")
		}
	})

	t.Run("rows affected", func(t *testing.T) {
		db, err := Open(filepath.Join(t.TempDir(), "prune.db"))
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		original := rowsAffected
		rowsAffected = func(sql.Result) (int64, error) { return 0, errors.New("rows affected failed") }
		t.Cleanup(func() { rowsAffected = original })

		if _, err := PruneExpired(context.Background(), db); err == nil {
			t.Fatal("PruneExpired must report a RowsAffected error")
		}
	})
}

func TestMigrateFailureBranches(t *testing.T) {
	t.Run("schema exec", func(t *testing.T) {
		db, err := sql.Open("sqlite", buildDSN(filepath.Join(t.TempDir(), "closed-migrate.db")))
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
		if err := migrate(db); err == nil {
			t.Fatal("migrate must report a schema Exec error")
		}
	})

	t.Run("ensure column", func(t *testing.T) {
		db := newRawSQLite(t)
		original := ensureColumnFn
		ensureColumnFn = func(*sql.DB, string, string, string) error { return errors.New("ensure failed") }
		t.Cleanup(func() { ensureColumnFn = original })

		if err := migrate(db); err == nil {
			t.Fatal("migrate must report an ensureColumn error")
		}
	})

	t.Run("index exec", func(t *testing.T) {
		db := newRawSQLite(t)
		originalEnsure := ensureColumnFn
		originalExec := execMigration
		ensureColumnFn = func(*sql.DB, string, string, string) error { return nil }
		execMigration = func(*sql.DB, string) (sql.Result, error) { return nil, errors.New("index failed") }
		t.Cleanup(func() {
			ensureColumnFn = originalEnsure
			execMigration = originalExec
		})

		if err := migrate(db); err == nil {
			t.Fatal("migrate must report an added-index Exec error")
		}
	})

	t.Run("backfill", func(t *testing.T) {
		db := newRawSQLite(t)
		originalEnsure := ensureColumnFn
		originalExec := execMigration
		originalBackfill := backfillUsernamesFn
		ensureColumnFn = func(*sql.DB, string, string, string) error { return nil }
		execMigration = func(*sql.DB, string) (sql.Result, error) { return nil, nil }
		backfillUsernamesFn = func(*sql.DB) error { return errors.New("backfill failed") }
		t.Cleanup(func() {
			ensureColumnFn = originalEnsure
			execMigration = originalExec
			backfillUsernamesFn = originalBackfill
		})

		if err := migrate(db); err == nil {
			t.Fatal("migrate must report a backfill error")
		}
	})
}

func TestBackfillUsernamesErrorBranches(t *testing.T) {
	t.Run("select pending", func(t *testing.T) {
		original := selectUsersWithoutUsernameFn
		selectUsersWithoutUsernameFn = func(*sql.DB) ([]userWithoutUsername, error) {
			return nil, errors.New("select failed")
		}
		t.Cleanup(func() { selectUsersWithoutUsernameFn = original })

		if err := backfillUsernames(nil); err == nil {
			t.Fatal("backfillUsernames must report a pending-select error")
		}
	})

	t.Run("select taken", func(t *testing.T) {
		originalPending := selectUsersWithoutUsernameFn
		originalTaken := selectTakenUsernamesFn
		selectUsersWithoutUsernameFn = func(*sql.DB) ([]userWithoutUsername, error) {
			return []userWithoutUsername{{id: 1, email: "a@example.com"}}, nil
		}
		selectTakenUsernamesFn = func(*sql.DB) (map[string]struct{}, error) {
			return nil, errors.New("taken failed")
		}
		t.Cleanup(func() {
			selectUsersWithoutUsernameFn = originalPending
			selectTakenUsernamesFn = originalTaken
		})

		if err := backfillUsernames(nil); err == nil {
			t.Fatal("backfillUsernames must report a taken-select error")
		}
	})

	t.Run("begin tx", func(t *testing.T) {
		originalPending := selectUsersWithoutUsernameFn
		originalTaken := selectTakenUsernamesFn
		originalBegin := beginTx
		selectUsersWithoutUsernameFn = func(*sql.DB) ([]userWithoutUsername, error) {
			return []userWithoutUsername{{id: 1, email: "a@example.com"}}, nil
		}
		selectTakenUsernamesFn = func(*sql.DB) (map[string]struct{}, error) {
			return map[string]struct{}{}, nil
		}
		beginTx = func(*sql.DB) (*sql.Tx, error) { return nil, errors.New("begin failed") }
		t.Cleanup(func() {
			selectUsersWithoutUsernameFn = originalPending
			selectTakenUsernamesFn = originalTaken
			beginTx = originalBegin
		})

		if err := backfillUsernames(nil); err == nil {
			t.Fatal("backfillUsernames must report a Begin error")
		}
	})

	t.Run("tx exec", func(t *testing.T) {
		db := newUsersDB(t)
		original := execTx
		execTx = func(*sql.Tx, string, ...any) (sql.Result, error) {
			return nil, errors.New("tx exec failed")
		}
		t.Cleanup(func() { execTx = original })

		if err := backfillUsernames(db); err == nil {
			t.Fatal("backfillUsernames must report a tx.Exec error")
		}
	})

	t.Run("commit", func(t *testing.T) {
		db := newUsersDB(t)
		original := commitTx
		commitTx = func(*sql.Tx) error { return errors.New("commit failed") }
		t.Cleanup(func() { commitTx = original })

		if err := backfillUsernames(db); err == nil {
			t.Fatal("backfillUsernames must report a Commit error")
		}
	})
}

func TestSelectUsersWithoutUsernameErrorBranches(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		db := newUsersDB(t)
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
		if _, err := selectUsersWithoutUsername(db); err == nil {
			t.Fatal("selectUsersWithoutUsername must report a Query error")
		}
	})

	t.Run("scan", func(t *testing.T) {
		db := newUsersDB(t)
		original := rowsScan
		rowsScan = func(*sql.Rows, ...any) error { return errors.New("scan failed") }
		t.Cleanup(func() { rowsScan = original })

		if _, err := selectUsersWithoutUsername(db); err == nil {
			t.Fatal("selectUsersWithoutUsername must report a Scan error")
		}
	})

	t.Run("iterate", func(t *testing.T) {
		db := newUsersDB(t)
		original := rowsErr
		rowsErr = func(*sql.Rows) error { return errors.New("iteration failed") }
		t.Cleanup(func() { rowsErr = original })

		if _, err := selectUsersWithoutUsername(db); err == nil {
			t.Fatal("selectUsersWithoutUsername must report an iteration error")
		}
	})
}

func TestSelectTakenUsernamesErrorBranches(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		db := newUsersDB(t)
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
		if _, err := selectTakenUsernames(db); err == nil {
			t.Fatal("selectTakenUsernames must report a Query error")
		}
	})

	t.Run("scan", func(t *testing.T) {
		db := newUsersDB(t)
		original := rowsScan
		rowsScan = func(*sql.Rows, ...any) error { return errors.New("scan failed") }
		t.Cleanup(func() { rowsScan = original })

		if _, err := selectTakenUsernames(db); err == nil {
			t.Fatal("selectTakenUsernames must report a Scan error")
		}
	})

	t.Run("iterate", func(t *testing.T) {
		db := newUsersDB(t)
		original := rowsErr
		rowsErr = func(*sql.Rows) error { return errors.New("iteration failed") }
		t.Cleanup(func() { rowsErr = original })

		if _, err := selectTakenUsernames(db); err == nil {
			t.Fatal("selectTakenUsernames must report an iteration error")
		}
	})
}

func TestDeriveUsernameCoversPrefixAndTruncation(t *testing.T) {
	if got := deriveUsername("123@example.com"); got != "user-123" {
		t.Fatalf("deriveUsername(digit start) = %q, want user-123", got)
	}
	long := strings.Repeat("a", 30) + "@example.com"
	got := deriveUsername(long)
	if len(got) != maxBackfilledUsernameLength {
		t.Fatalf("deriveUsername(long) = %q (len %d), want len %d", got, len(got), maxBackfilledUsernameLength)
	}
}

func TestUniqueUsernameTruncatesAndFallsBack(t *testing.T) {
	candidate := strings.Repeat("a", maxBackfilledUsernameLength)
	taken := map[string]struct{}{candidate: {}}
	if got := uniqueUsername(candidate, taken); len(got) > maxBackfilledUsernameLength {
		t.Fatalf("uniqueUsername returned %q (len %d), over the limit", got, len(got))
	}

	fallbackTaken := map[string]struct{}{"bob": {}}
	for suffix := 2; suffix < 1000; suffix++ {
		fallbackTaken[fmt.Sprintf("bob-%d", suffix)] = struct{}{}
	}
	got := uniqueUsername("bob", fallbackTaken)
	if !strings.HasPrefix(got, "user-") {
		t.Fatalf("uniqueUsername fallback = %q, want user- prefix", got)
	}
}

func TestEnsureColumnFailureBranches(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		db := newUsersDB(t)
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
		if err := ensureColumn(db, "users", "username", "TEXT"); err == nil {
			t.Fatal("ensureColumn must report a Query error")
		}
	})

	t.Run("scan", func(t *testing.T) {
		db := newUsersDB(t)
		original := rowsScan
		rowsScan = func(*sql.Rows, ...any) error { return errors.New("scan failed") }
		t.Cleanup(func() { rowsScan = original })

		if err := ensureColumn(db, "users", "username", "TEXT"); err == nil {
			t.Fatal("ensureColumn must report a Scan error")
		}
	})

	t.Run("iterate", func(t *testing.T) {
		db := newUsersDB(t)
		original := rowsErr
		rowsErr = func(*sql.Rows) error { return errors.New("iteration failed") }
		t.Cleanup(func() { rowsErr = original })

		if err := ensureColumn(db, "users", "username", "TEXT"); err == nil {
			t.Fatal("ensureColumn must report an iteration error")
		}
	})

	t.Run("alter", func(t *testing.T) {
		db := newUsersDB(t)
		if err := ensureColumn(db, "missing_table", "new_column", "TEXT"); err == nil {
			t.Fatal("ensureColumn must report an ALTER error for a missing table")
		}
	})
}

func writeEmptyFile(path string) error {
	return os.WriteFile(path, []byte("not a real db"), 0o600)
}

func newRawSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", buildDSN(filepath.Join(t.TempDir(), "raw.db")))
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newUsersDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newRawSQLite(t)
	if _, err := db.Exec(`
CREATE TABLE users (
id INTEGER PRIMARY KEY,
email TEXT,
username TEXT
);
INSERT INTO users (email, username) VALUES ('pending@example.com', '');
INSERT INTO users (email, username) VALUES ('taken@example.com', 'taken');
`); err != nil {
		t.Fatalf("prepare users table: %v", err)
	}
	return db
}
