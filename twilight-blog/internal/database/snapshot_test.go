package database

import (
	"os"
	"path/filepath"
	"testing"
)

// 本文件钉住 Snapshot 的两条性质，它们是「备份可用」的全部依据：
//
//   - 快照里**包含**还留在 -wal 里、尚未 checkpoint 的已提交写入；
//   - 快照是一个独立文件，之后源库怎么变都与它无关。
//
// 第一条是它存在的理由。直接拷贝 blog.db 会丢掉那批写入，而结果看起来完全正常
// ——能打开、有表、有数据，只是少了最近几条。这种归档要到还原那天才暴露，那时
// 已经晚了。所以这条断言必须打在**副作用**上（快照里查得到那一行），而不是
// 「Snapshot 返回了 nil」。

// snapshotSource 造一个源库并写入一行可识别的数据，返回源路径与一个「还没关闭」
// 的连接。
//
// 刻意保持连接打开：关闭时会触发 checkpoint，把 -wal 里的内容并回主文件，那样
// 就测不出「VACUUM INTO 能拿到 WAL 里的数据」了——而那是这个函数存在的全部理由。
func snapshotSource(t *testing.T, dir string) (string, func()) {
	t.Helper()

	src := filepath.Join(dir, "blog.db")
	db, err := Open(src)
	if err != nil {
		t.Fatalf("创建源库失败: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO users (email, username, nickname, password_hash, is_admin, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)`,
		"", "admin", "只在 WAL 里的站长", "$2a$10$notarealhash", 1, 1,
	); err != nil {
		db.Close()
		t.Fatalf("写入源库失败: %v", err)
	}
	return src, func() { db.Close() }
}

func TestSnapshotCapturesWritesStillInTheWAL(t *testing.T) {
	dir := t.TempDir()
	src, closeSource := snapshotSource(t, dir)

	// 前提检查：那一行必须真的还在 WAL 里，否则这条用例证明不了任何事。
	wal, err := os.Stat(src + "-wal")
	if err != nil || wal.Size() == 0 {
		closeSource()
		t.Fatalf("前提不成立：%s 不存在或为空（err=%v）", src+"-wal", err)
	}

	dst := filepath.Join(dir, "snapshot.db")
	if err := Snapshot(src, dst); err != nil {
		closeSource()
		t.Fatalf("Snapshot() 报错: %v", err)
	}
	// 源库此时仍然打开着，也就是说快照是在「有人可能正在写」的状态下拿到的。
	closeSource()

	snapshot, err := Open(dst)
	if err != nil {
		t.Fatalf("打开快照失败: %v", err)
	}
	defer snapshot.Close()

	var nickname string
	if err := snapshot.QueryRow(`SELECT nickname FROM users WHERE is_admin = 1`).Scan(&nickname); err != nil {
		t.Fatalf("快照里读不到那一行: %v", err)
	}
	if nickname != "只在 WAL 里的站长" {
		t.Fatalf("快照里的昵称 = %q", nickname)
	}
}

// TestSnapshotIsIndependentOfTheSource 钉住「快照是一份独立文件」。
//
// 备份写完之后的任何写入都不该出现在已经落盘的快照里——否则「这份归档是几点钟
// 的状态」这个问题就没有答案，而还原时最需要回答的正是它。
func TestSnapshotIsIndependentOfTheSource(t *testing.T) {
	dir := t.TempDir()
	src, closeSource := snapshotSource(t, dir)
	defer closeSource()

	dst := filepath.Join(dir, "snapshot.db")
	if err := Snapshot(src, dst); err != nil {
		t.Fatalf("Snapshot() 报错: %v", err)
	}

	// 快照之后源库继续被写：改名。
	source, err := Open(src)
	if err != nil {
		t.Fatalf("重新打开源库失败: %v", err)
	}
	if _, err := source.Exec(`UPDATE users SET nickname = '快照之后改的' WHERE is_admin = 1`); err != nil {
		source.Close()
		t.Fatalf("更新源库失败: %v", err)
	}
	source.Close()

	snapshot, err := Open(dst)
	if err != nil {
		t.Fatalf("打开快照失败: %v", err)
	}
	defer snapshot.Close()

	var nickname string
	if err := snapshot.QueryRow(`SELECT nickname FROM users WHERE is_admin = 1`).Scan(&nickname); err != nil {
		t.Fatalf("快照里读不到那一行: %v", err)
	}
	if nickname != "只在 WAL 里的站长" {
		t.Fatalf("快照跟着源库变了: %q", nickname)
	}
}

// TestSnapshotRejectsUnusableInputs 覆盖三条拒绝路径。
//
//   - 源库不存在：报错而不是产出一个空文件；
//   - 目标已存在：VACUUM INTO 本来就不接受已存在的目标，而这条前置检查把它变成
//     一句可读的错误（否则用户看到的是一句 SQLite 的内部报错）。它同时保证了
//     「这份快照是本次生成的」，而不是上一次留下的那份被当成本次结果；
//   - 源库不是数据库：报错而不是产出一个 0 字节的快照。
func TestSnapshotRejectsUnusableInputs(t *testing.T) {
	dir := t.TempDir()

	if err := Snapshot(filepath.Join(dir, "missing.db"), filepath.Join(dir, "out.db")); err == nil {
		t.Fatal("源库不存在时应当报错")
	}

	src, closeSource := snapshotSource(t, dir)
	defer closeSource()

	existing := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(existing, []byte("do not touch"), 0o600); err != nil {
		t.Fatalf("准备目标文件失败: %v", err)
	}
	err := Snapshot(src, existing)
	if err == nil {
		t.Fatal("目标已存在时应当报错")
	}
	if body, readErr := os.ReadFile(existing); readErr != nil || string(body) != "do not touch" {
		t.Fatalf("被拒绝的快照不该动那个已存在的文件: body=%q err=%v", body, readErr)
	}

	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatalf("准备坏库失败: %v", err)
	}
	err = Snapshot(garbage, filepath.Join(dir, "from-garbage.db"))
	if err == nil {
		t.Fatal("源库不是 SQLite 库时应当报错")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "from-garbage.db")); statErr == nil {
		t.Fatal("失败的快照不该留下文件")
	}
}
