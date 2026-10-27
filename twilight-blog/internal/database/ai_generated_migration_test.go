package database

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// createLegacyAIGeneratedTable 建出只有**一个** ai_generated 列的老形状。
//
// ai_generated_en 是后拆出来的：老库里「标过 AI」这件事只有一份，迁移必须把
// 它复制给英文版，否则升级后英文页会突然失去声明。
func createLegacyAIGeneratedTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE articles (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT    NOT NULL,
    content     TEXT    NOT NULL DEFAULT '',
    title_en    TEXT    NOT NULL DEFAULT '',
    content_en  TEXT    NOT NULL DEFAULT '',
    ai_generated INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    deleted_at  INTEGER
);`); err != nil {
		t.Fatalf("create legacy articles table: %v", err)
	}
}

func seedLegacyArticle(t *testing.T, db *sql.DB, title string, aiGenerated int) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO articles (title, content, title_en, content_en, ai_generated, created_at, updated_at)
VALUES (?, '正文', 'body', 'body', ?, 1, 1);
`, title, aiGenerated); err != nil {
		t.Fatalf("seed %s: %v", title, err)
	}
}

func openLegacyAIGeneratedDB(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	createLegacyAIGeneratedTable(t, raw)
	seedLegacyArticle(t, raw, "机器写的", 1)
	seedLegacyArticle(t, raw, "人写的", 0)
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}
}

func englishAIFlag(t *testing.T, db *sql.DB, title string) bool {
	t.Helper()
	var value int
	if err := db.QueryRow(`SELECT ai_generated_en FROM articles WHERE title = ?`, title).Scan(&value); err != nil {
		t.Fatalf("read ai_generated_en of %s: %v", title, err)
	}
	return value != 0
}

// TestUpgradeCopiesTheGlobalAIFlagIntoTheEnglishColumn 覆盖一次性回填：
// 老库里标过 AI 的文章，升级后中英文都仍然是 AI；没标的保持 false。
func TestUpgradeCopiesTheGlobalAIFlagIntoTheEnglishColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-ai.db")
	openLegacyAIGeneratedDB(t, path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() on a pre-ai_generated_en database error = %v", err)
	}
	defer db.Close()

	if !hasColumn(t, db, "articles", "ai_generated_en") {
		t.Fatal("articles.ai_generated_en was not added to the existing table")
	}
	if !englishAIFlag(t, db, "机器写的") {
		t.Fatal("a legacy AI-marked article lost its English marker during the upgrade")
	}
	if englishAIFlag(t, db, "人写的") {
		t.Fatal("a legacy hand-written article became AI-marked in English")
	}
}

// TestTheBackfillOnlyRunsOnce 钉住「只在补列那次回填」。
//
// 如果每次启动都执行 UPDATE … = ai_generated，站长在英文页明确取消勾选后，
// 下次重启又会被改回 true——声明会自己复活，而界面上看不出是谁改的。
func TestTheBackfillOnlyRunsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-ai-once.db")
	openLegacyAIGeneratedDB(t, path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("first Open(): %v", err)
	}
	if _, err := db.Exec(`UPDATE articles SET ai_generated_en = 0 WHERE title = '机器写的'`); err != nil {
		t.Fatalf("clear the English marker: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("second Open(): %v", err)
	}
	defer db.Close()
	if englishAIFlag(t, db, "机器写的") {
		t.Fatal("the backfill ran again and revived a marker the operator had cleared")
	}
}

// TestFreshDatabasesGetAnEmptyEnglishFlag 确认新建库不经过回填也是 0。
func TestFreshDatabasesGetAnEmptyEnglishFlag(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "fresh-ai.db"))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
INSERT INTO articles (title, content, ai_generated, created_at, updated_at)
VALUES ('新文章', '正文', 0, 1, 1);
`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if englishAIFlag(t, db, "新文章") {
		t.Fatal("a fresh database must default ai_generated_en to 0")
	}
}

// TestMigrateReportsColumnProbeAndBackfillFailures 覆盖迁移新增的两处错误：
// 探测列失败（此时不知道要不要回填，只能中止）与回填 UPDATE 失败。
func TestMigrateReportsColumnProbeAndBackfillFailures(t *testing.T) {
	t.Run("column probe", func(t *testing.T) {
		db := newUsersDB(t)
		original := columnExistsFn
		columnExistsFn = func(*sql.DB, string, string) (bool, error) {
			return false, errors.New("probe failed")
		}
		t.Cleanup(func() { columnExistsFn = original })

		if err := migrate(db); err == nil {
			t.Fatal("migrate must stop when it cannot tell whether the column exists")
		}
	})

	t.Run("backfill", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy-ai-backfill.db")
		openLegacyAIGeneratedDB(t, path)
		db, err := sql.Open("sqlite", buildDSN(path))
		if err != nil {
			t.Fatalf("open raw database: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		original := execMigration
		execMigration = func(database *sql.DB, statement string) (sql.Result, error) {
			if strings.Contains(statement, "UPDATE articles SET ai_generated_en") {
				return nil, errors.New("write refused")
			}
			return database.Exec(statement)
		}
		t.Cleanup(func() { execMigration = original })

		err = migrate(db)
		if err == nil || !strings.Contains(err.Error(), "backfill ai_generated_en") {
			t.Fatalf("err = %v, want a backfill error", err)
		}
	})
}
