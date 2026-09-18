package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"blog-engine/internal/database"
)

// inTempWorkdir 把进程的工作目录切到一个临时目录，返回该目录。
//
// backup / restore 的所有路径都是**相对工作目录**的（settings.json、blog.db、
// media），与主进程完全一致。所以测它们就得真的换掉 cwd——用一个「把路径当参数
// 传进去」的版本去测，测的就不是同一份代码了，而这里最容易错的恰恰是路径。
//
// 副作用是这些测试不能并行（这个包里不能用 t.Parallel），这一点是刻意的：
// 换 cwd 本来就是全局状态。
//
// 用 os.MkdirTemp(cliTestRoot, …) 而**不是** t.TempDir()：还原流程会调用
// logging.Infof，而日志文件是在**当前工作目录**下打开、并在进程剩余生命周期里
// 一直持有句柄的（logging 包的设计如此）。句柄落在 t.TempDir() 里时，Windows
// 上的自动清理会因为「文件仍被打开」而失败——于是一条断言全过的用例被标成失败。
// 建在 cliTestRoot 下就没有这个问题：那个根目录由 TestMain 统一处理，删不掉
// 也只是留在 %TEMP% 里。与 admin_command_test.go 的 useTempInstance 同一套做法。
func inTempWorkdir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp(cliTestRoot, "instance-")
	if err != nil {
		t.Fatalf("创建临时工作目录失败: %v", err)
	}

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("读取当前工作目录失败: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("切换到临时工作目录失败: %v", err)
	}

	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("恢复工作目录失败: %v", err)
		}
	})
	return dir
}

// seedTree 在 root 下造一棵小文件树。
func seedTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("准备测试数据失败 (%s): %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("准备测试数据失败 (%s): %v", name, err)
		}
	}
}

// seedSite 在当前工作目录里造一个最小的、结构真实的站点。
//
// blog.db 必须是**真正的 SQLite 数据库**：备份走的是 VACUUM INTO，它要求源文件
// 是一个 SQLite 能打开的库。随手写几个字节进去再断言「备份成功」，测的就不是
// 同一条路径——那种文件会在 Snapshot 里立刻失败。
func seedSite(t *testing.T, mediaFiles map[string]string) {
	t.Helper()

	db, err := database.Open(databasePath)
	if err != nil {
		t.Fatalf("创建测试数据库失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO users (email, username, nickname, password_hash, is_admin, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)`,
		"", "admin", "站长", "$2a$10$notarealhash", 1, 1,
	); err != nil {
		db.Close()
		t.Fatalf("写入测试数据失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}

	if err := os.WriteFile(settingsPath,
		[]byte(`{"media_dir":"media","site_description":"测试站点"}`), 0o600); err != nil {
		t.Fatalf("写 settings.json 失败: %v", err)
	}
	seedTree(t, "media", mediaFiles)
}

// adminNickname 读当前库里管理员的昵称。
//
// 用它断言「数据库内容真的回来了」，而不是「blog.db 这个文件存在」——后者在
// 一次把空库放回去的还原里同样成立。
func adminNickname(t *testing.T) string {
	t.Helper()

	db, err := database.Open(databasePath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	var nickname string
	if err := db.QueryRow(`SELECT nickname FROM users WHERE is_admin = 1`).Scan(&nickname); err != nil {
		t.Fatalf("读管理员昵称失败: %v", err)
	}
	return nickname
}

// setAdminNickname 直接改库里的昵称，用来模拟「备份之后又发生了写入」——
// 还原之后这一改动必须消失，那是「还原真的生效了」的证据。
func setAdminNickname(t *testing.T, nickname string) {
	t.Helper()

	db, err := database.Open(databasePath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`UPDATE users SET nickname = ? WHERE is_admin = 1`, nickname); err != nil {
		t.Fatalf("更新管理员昵称失败: %v", err)
	}
}

// countFiles 数一个目录树里的常规文件数。
//
// 这一轮的功能全是文件层面的搬运，接口状态码完全反映不出文件落没落对地方，
// 所以断言要打在磁盘上。
func countFiles(t *testing.T, root string) int {
	t.Helper()

	total := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		total++
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	return total
}

// exists 报告路径是否存在（含目录）。
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
