package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// createLegacyArticlesTable 建出 articles 表**改动之前**的形状：没有 cover_media_id。
//
// 这正是让迁移测试有意义的前提——列只可能通过 addedColumns 出现。
// 如果哪天有人只改了 schemaSQL 里的建表语句而忘了登记 addedColumns，
// 新库（走建表语句）一切正常，老库却在启动时炸出 "no such column: cover_media_id"，
// 而本地开发几乎总是删库重建，正好绕开了这条路径。这个测试专门堵住那条路径。
func createLegacyArticlesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TABLE articles (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT    NOT NULL,
    content    TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    deleted_at INTEGER
);`); err != nil {
		t.Fatalf("create legacy articles table: %v", err)
	}
}

// TestUpgradeAddsCoverMediaIDToAnOldDatabase 覆盖「加一列」规则的后半段：
// 一个在 cover_media_id 存在之前建的库，其 articles 表不会被
// CREATE TABLE IF NOT EXISTS 动到，因此只有 addedColumns 能把它带过来。
//
// 反证方式：把 schema.go 里 {"articles", "cover_media_id", ...} 那一行删掉 →
// hasColumn 立刻为 false，本条失败。
func TestUpgradeAddsCoverMediaIDToAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-cover.db")

	raw, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	createLegacyArticlesTable(t, raw)
	if _, err := raw.Exec(`
INSERT INTO articles (title, content, created_at, updated_at)
VALUES ('旧文章', '正文', 1, 1);
`); err != nil {
		t.Fatalf("seed the legacy article: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() on a pre-cover_media_id database error = %v", err)
	}
	defer db.Close()

	if !hasColumn(t, db, "articles", "cover_media_id") {
		t.Fatal("articles.cover_media_id was not added to the existing table")
	}

	// 已有行必须拿到 NULL（= 没有头图）。带 REFERENCES 的补列不写 DEFAULT
	// 正是为了满足 SQLite 的要求，这里是那条约束的可观察结果。
	var cover any
	if err := db.QueryRow(`SELECT cover_media_id FROM articles WHERE title = '旧文章'`).Scan(&cover); err != nil {
		t.Fatalf("read cover_media_id of the legacy row: %v", err)
	}
	if cover != nil {
		t.Fatalf("a pre-existing article got a cover (cover_media_id = %v), want NULL", cover)
	}
}

// TestCoverMediaIDCascadesToNullOnTheUpgradedColumn 确认**补出来的**那一列
// 同样带着 ON DELETE SET NULL —— ALTER TABLE ADD COLUMN 是逐字复制定义串的，
// 若定义里漏掉 ON DELETE SET NULL，列本身照样加得上、也不报任何错，
// 只是删媒体时文章会留下一个指向不存在媒体的悬空 id（前端拿它拼 URL 得到 404 图）。
//
// 这个断言必须打在 ALTER 出来的列上（而不是新建库里建表语句给的列），
// 因为两条路径是两份独立写下的定义，只有这里覆盖的是老库实际拿到的那一份。
func TestCoverMediaIDCascadesToNullOnTheUpgradedColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-cover-fk.db")

	raw, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	createLegacyArticlesTable(t, raw)
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	// media 表由 schemaSQL 建好；这里只需要一行能指向的记录。
	if _, err := db.Exec(`
INSERT INTO media (id, storage_name, original_name, media_type, mime_type, size, created_at)
VALUES (9, 'store-9.png', 'cover.png', 'image', 'image/png', 123, 1);
INSERT INTO articles (id, title, content, created_at, updated_at, cover_media_id)
VALUES (3, '带头图的旧文章', '正文', 1, 1, 9);
`); err != nil {
		t.Fatalf("seed media + article with a cover: %v", err)
	}

	// 删掉那张媒体：文章上的引用必须被数据库自己清成 NULL。
	if _, err := db.Exec(`DELETE FROM media WHERE id = 9`); err != nil {
		t.Fatalf("delete the media row: %v", err)
	}

	var cover any
	if err := db.QueryRow(`SELECT cover_media_id FROM articles WHERE id = 3`).Scan(&cover); err != nil {
		t.Fatalf("read cover_media_id after the media was deleted: %v", err)
	}
	if cover != nil {
		t.Fatalf("cover_media_id = %v after its media was deleted, want NULL — the added column is missing ON DELETE SET NULL", cover)
	}
}
