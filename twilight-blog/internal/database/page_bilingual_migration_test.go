package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// createLegacyPagesTable 建出双语之前的老形状：只有 title / content。
//
// 与 articles 的 *_en 列同理，CREATE TABLE IF NOT EXISTS 不会给已有表补列，
// 老库上的这两列只可能通过 addedColumns 出现——这正是本测试要钉住的路径。
func createLegacyPagesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE pages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT    NOT NULL,
    content     TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    deleted_at  INTEGER
);`); err != nil {
		t.Fatalf("create legacy pages table: %v", err)
	}
}

func TestUpgradeAddsPageEnglishColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-pages.db")

	raw, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	createLegacyPagesTable(t, raw)
	if _, err := raw.Exec(`
INSERT INTO pages (title, content, created_at, updated_at)
VALUES ('老页面', '老正文', 1, 1);
`); err != nil {
		t.Fatalf("seed legacy page: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() on a pre-bilingual pages database error = %v", err)
	}
	defer db.Close()

	for _, column := range []string{"title_en", "content_en"} {
		if !hasColumn(t, db, "pages", column) {
			t.Fatalf("pages.%s was not added to the existing table", column)
		}
	}

	// 老数据必须原样保留，新列默认空串（不是 NULL）——读取侧据此判断「没有英文版」。
	var title, content, titleEN, contentEN string
	if err := db.QueryRow(`SELECT title, content, title_en, content_en FROM pages WHERE title = '老页面'`).
		Scan(&title, &content, &titleEN, &contentEN); err != nil {
		t.Fatalf("read upgraded page: %v", err)
	}
	if title != "老页面" || content != "老正文" || titleEN != "" || contentEN != "" {
		t.Fatalf("upgraded page = (%q, %q, %q, %q)", title, content, titleEN, contentEN)
	}

	// 站长补写英文后再开一次：补列是幂等的，不能把已有译文重置成空串。
	if _, err := db.Exec(`UPDATE pages SET title_en = 'Old page', content_en = 'Old body' WHERE title = '老页面'`); err != nil {
		t.Fatalf("write english columns: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer reopened.Close()
	if err := reopened.QueryRow(`SELECT title_en, content_en FROM pages WHERE title = '老页面'`).
		Scan(&titleEN, &contentEN); err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	if titleEN != "Old page" || contentEN != "Old body" {
		t.Fatalf("a restart reset page english = (%q, %q)", titleEN, contentEN)
	}
}

// TestFreshPageTableHasEnglishColumns 确认新建库直接就有这两列，
// 而不是依赖升级路径才补上。
func TestFreshPageTableHasEnglishColumns(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "fresh-pages.db"))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer db.Close()

	for _, column := range []string{"title_en", "content_en"} {
		if !hasColumn(t, db, "pages", column) {
			t.Fatalf("fresh database is missing pages.%s", column)
		}
	}
}
