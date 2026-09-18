package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 本文件是还原命令用到的文件系统原语。它们被单独放一处有两个理由：
// 这些操作是整条链路里**唯一不可逆**的动作，值得单独测；而且「先挪开旧的、
// 再把新的放进来、失败要能退回去」这套顺序，写错了不会编译失败，只会丢数据。

// swapIn 把 src 换到 dst 位置。keepOld 为真时把原有内容改名保留，返回保留路径。
//
// 为什么不是一句 os.Rename：dst 通常已经存在（这正是「还原」的含义），而 Rename
// 覆盖一个**非空目录**在 Windows 上直接失败、在 POSIX 上会把旧文件留成无法回收的
// 孤儿。所以分三步走：旧的挪开 → 新的就位 → 再决定旧的怎么办。
//
// 中间任何一步失败都会把旧数据放回原位：一次失败的还原应该留下一个**能用的站点**，
// 而不是一个「什么都没有」的目录。
func swapIn(src, dst string, keepOld bool) (kept string, err error) {
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("the staged copy of %s is missing", dst)
	}

	var parked string
	if _, err := os.Lstat(dst); err == nil {
		parked = dst + ".replaced-" + nowStamp()
		if err := os.Rename(dst, parked); err != nil {
			return "", fmt.Errorf("move the existing %s aside: %w", dst, err)
		}
	}

	if err := place(src, dst); err != nil {
		if parked != "" {
			if backErr := os.Rename(parked, dst); backErr != nil {
				// 恢复也失败时两个错误都要报出来：此刻磁盘上同时存在
				// 旧数据（在 parked 下）与一个半成品，现场的人需要知道
				// 哪个文件能救回来。
				return "", fmt.Errorf("put %s in place: %w (and moving the previous copy back failed too: %v)", dst, err, backErr)
			}
		}
		return "", fmt.Errorf("put %s in place: %w", dst, err)
	}

	if parked == "" {
		return "", nil
	}
	if keepOld {
		return parked, nil
	}
	if err := os.RemoveAll(parked); err != nil {
		// 删不掉不影响还原是否成功，报出来即可——反正数据已经换好了。
		return parked, nil
	}
	return "", nil
}

// place 把 src 搬到 dst，必要时退化成「拷贝 + 删源」。
//
// Rename 只在同一卷内是原子的。媒体目录完全可能挂在另一块盘上（把大文件放数据盘
// 是常规做法），那时 Rename 会以跨设备错误失败——而「跨设备」这个判断本身要写
// 平台相关代码才能准确做出来，不如直接试一次、失败就退化成拷贝：这条路慢，但只有
// 跨卷时才会走到，而它比写错一个平台判断要可靠得多。
func place(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	if err := copyTree(src, dst); err != nil {
		// 拷贝到一半失败会留下一个不完整的 dst，而它看起来像还原成功了。
		// 清掉它，让失败保持「什么都没发生」的样子。
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

// copyTree 递归拷贝文件与目录。
//
// 符号链接一律跳过：暂存目录里的内容全部来自归档，而解包端只写出常规文件与目录，
// 所以这里遇到链接只可能是宿主环境自己放进来的东西，拷它没有意义。
func copyTree(src, dst string) error {
	// 目标落在源内部时必须拒绝，而且要在**遍历之前**。否则每拷一层就多出一层
	// 要拷的东西——表现是进程卡死、磁盘被写满，而不是一个错误。
	// 这个判断必须用绝对路径：调用方传进来的两个路径常常一个是相对的。
	if samePath(src, dst) || within(src, dst) {
		return fmt.Errorf("refusing to copy %s into itself (%s)", src, dst)
	}

	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if !info.IsDir() {
		return copyFile(src, dst, 0o600)
	}

	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	children, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	for _, child := range children {
		from := filepath.Join(src, child.Name())
		to := filepath.Join(dst, child.Name())

		// 用 DirEntry 自带的类型判断而不是再 Stat 一次：它由 ReadDir 一次读出，
		// 既少一次系统调用，也少一个「条目在遍历途中消失」的失败分支。
		if child.IsDir() {
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		if !child.Type().IsRegular() {
			continue
		}
		if err := copyFile(from, to, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// copyFile 拷一个文件，权限固定 0600（内容与归档同源，含凭据）。
func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// removeSQLiteSidecars 删掉数据库旁边的 -wal / -shm / -journal。
//
// 必须在新的 blog.db 落地**之前**做，不能省。这三个文件属于**还原之前**那一份库：
//
//   - -wal 里装着还原前那些已提交的事务。让它与刚放下的库共存，最好的情况是它被
//     判定为无效后丢弃（白占地方，还会让 -shm 与库对不上），最坏的情况是那些帧被
//     回放进新库——写进去的是备份之后的修改，或者干脆把库弄坏；
//   - -shm 是共享内存索引，里面的页号与旧库强绑定；
//   - -journal 是回滚日志模式下的残留，同理会把旧页写回新库。
//
// 三种结果都不值得赌。删掉是确定的，而且删掉的只是缓存性质的东西——真正的数据
// 全在 blog.db 里。
func removeSQLiteSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove the stale %s%s: %w", path, suffix, err)
		}
	}
	return nil
}

// samePath 报告两个路径是否指向同一个位置。
//
// 先转绝对路径再比，光比字符串是不够的：调用方传进来的两个路径常常一个是相对的、
// 一个是绝对的，而那种差异会让「拷到自己身上」这个判断被悄悄漏掉。
// Windows 上路径大小写不敏感，所以这里不区分大小写地比。
func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(absA, absB)
}

// nowStamp 返回用于给「挪开的旧数据」命名的时间戳。
func nowStamp() string {
	return time.Now().UTC().Format("20060102-150405")
}
