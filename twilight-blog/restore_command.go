package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"blog-engine/internal/backup"
	"blog-engine/internal/database"
	"blog-engine/internal/logging"
	"blog-engine/internal/pidfile"
	"blog-engine/internal/search"
	"blog-engine/internal/session"
)

// 本文件实现 `<executable> restore <archive>` 子命令。
//
// 它是 backup 的反向操作，也是这个程序里**唯一一个会不可逆地替换整站数据的命令**。
// 三条安全措施都来自这个前提：
//
//   - 动数据之前先自动打一份「还原前安全备份」，它就是回滚点；
//   - 换位之前先把整个归档解包并逐项校验（sha256 对账、路径守卫、体积封顶），
//     宁可白解一遍也不在半途失败；
//   - 默认吊销全部会话，理由见 revokeRestoredSessions。
const restoreCommandUsage = `Usage:
  <executable> restore <archive|-> [options]

Replaces this site's data with the contents of a backup: the database, the media
directory and (unless --keep-settings) settings.json. Stop the site first — a
running instance keeps writing through its old file handles, and the search index
would be re-synced over the restored database.

A safety backup of the current state is taken first, so a restore that turns out
to have been a mistake can be rolled back.

Options:
  --yes                Do not ask for confirmation. Required when stdin is not a
                       terminal: the command never treats "nobody could answer"
                       as consent.
  --keep-settings      Keep this host's settings.json instead of the archived one.
  --media-dir <dir>    Restore the media into <dir> (a path inside the working
                       directory) instead of this host's settings.json media_dir.
                       Only valid when the archive's settings.json is restored;
                       the restored file's media_dir is rewritten to match, so the
                       two never disagree.
  --accept-archive-settings
                       Restore the archived settings.json even when it weakens
                       this host's security (disables rate limiting, widens
                       trusted_proxies, sets cookie_secure=never, points a
                       transcoding tool at another executable). Without this flag
                       such an archive is refused.
  --keep-sessions      Keep the session rows from the archive. By default every
                       session is revoked: a restore rolls the sessions table back
                       to the moment of the backup, which would bring back tokens
                       that had been revoked after it.
  --no-safety-backup   Skip the automatic pre-restore backup. Without it there is
                       no way back.
  --dry-run            Verify the archive and print what would be written, without
                       changing anything.
  --force              Proceed even if a running instance was detected.

The archive may be given as "-" to read it from stdin, so a backup can be piped
straight from another machine.

The media directory is always chosen by THIS host: the archive's media_dir is
never used as a destination path (it could point anywhere on the filesystem).
`

// restoreOptions 是 restore 子命令解析出来的参数。
type restoreOptions struct {
	archive        string
	yes            bool
	keepSettings   bool
	keepSessions   bool
	noSafetyBackup bool
	dryRun         bool
	force          bool
	// mediaDir 是显式的媒体落点；为空表示用本机 settings.json 的 media_dir。
	mediaDir string
	// acceptArchiveSettings 允许采用一份会削弱本机防护的归档配置（见
	// restore_settings.go 的 validateArchiveSettings）。
	acceptArchiveSettings bool
}

// runRestoreCommand 执行 restore 子命令，返回进程退出码。
func runRestoreCommand(args []string) int {
	opts, err := parseRestoreArgs(args)
	if err != nil {
		fmt.Fprintln(stderr(), err)
		return exitUsageOrValidation
	}

	if err := logging.Configure(""); err != nil {
		fmt.Fprintf(stderr(), "configure logging: %v\n", err)
		return exitFailure
	}

	if err := performRestore(opts); err != nil {
		fmt.Fprintf(stderr(), "restore failed: %v\n", err)
		return exitFailure
	}
	return 0
}

// parseRestoreArgs 解析参数。
//
// 归档用位置参数而不是 -i/--input：这条命令有且只有一个必填输入，把它写进用法行
// 比让人记住一个选项名更省事，也让 `restore backup.tar.gz --dry-run` 这种最自然的
// 读法成立。
func parseRestoreArgs(args []string) (restoreOptions, error) {
	var opts restoreOptions

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--yes" || arg == "-y":
			opts.yes = true
		case arg == "--keep-settings":
			opts.keepSettings = true
		case arg == "--keep-sessions":
			opts.keepSessions = true
		case arg == "--no-safety-backup":
			opts.noSafetyBackup = true
		case arg == "--dry-run":
			opts.dryRun = true
		case arg == "--force":
			opts.force = true
		case arg == "--accept-archive-settings":
			opts.acceptArchiveSettings = true
		case arg == "--media-dir":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("missing value for --media-dir\n\n%s", restoreCommandUsage)
			}
			i++
			if err := setRestoreMediaDir(&opts, args[i]); err != nil {
				return opts, err
			}
		case strings.HasPrefix(arg, "--media-dir="):
			if err := setRestoreMediaDir(&opts, strings.TrimPrefix(arg, "--media-dir=")); err != nil {
				return opts, err
			}
		case strings.HasPrefix(arg, "-") && arg != "-":
			// `-` 单独出现是合法输入（从 stdin 读归档），其余以 - 开头的都当参数。
			return opts, fmt.Errorf("unknown argument %q\n\n%s", arg, restoreCommandUsage)
		default:
			if opts.archive != "" {
				return opts, fmt.Errorf("only one archive can be restored at a time (got %q and %q)", opts.archive, arg)
			}
			opts.archive = arg
		}
	}

	if opts.archive == "" {
		return opts, fmt.Errorf("missing the archive to restore\n\n%s", restoreCommandUsage)
	}
	return opts, nil
}

// setRestoreMediaDir 记下 --media-dir 的值，拒绝空值与重复指定。
//
// 只做「是不是一个可用的本地相对路径」这一层校验；与工作目录相关的完整校验在
// restoreMediaDir 里（那需要知道 settingsPath，属于运行期知识）。
func setRestoreMediaDir(opts *restoreOptions, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("the --media-dir path must not be empty\n\n%s", restoreCommandUsage)
	}
	if opts.mediaDir != "" {
		return fmt.Errorf("--media-dir specified more than once")
	}
	opts.mediaDir = value
	return nil
}

// performRestore 跑完整个还原流程。
//
// 顺序是刻意的：
//
//  1. 读清单——能在动手之前就发现「这不是本站的归档」或「格式太新」；
//  2. 挡下「服务还在跑」；
//  3. 完整解包到暂存目录并逐项校验；
//  4. 到这里才问用户（先给他「归档是好的」这个事实，再让他决定）；
//  5. 打安全备份；
//  6. 换位、吊销会话。
func performRestore(opts restoreOptions) error {
	source, closeSource, err := openArchiveSource(opts.archive)
	if err != nil {
		return err
	}
	defer closeSource()

	reader, err := backup.Open(source)
	if err != nil {
		return err
	}
	manifest := reader.Manifest()
	reportManifest(stdout(), manifest)

	// 选项之间的自相矛盾要在解包、问询、打安全备份**之前**挡下：否则用户答完
	// 「yes」、白等一份安全备份，才被告知这个组合本来就不成立。
	if err := checkRestoreOptions(manifest, opts); err != nil {
		return err
	}

	if opts.dryRun {
		if info, running := pidfile.Holder(pidfile.FileName); running {
			fmt.Fprintf(stdout(), "Note: this site is running right now (pid %d); a real restore would refuse to proceed.\n", info.PID)
		}
	} else if err := refuseIfRunning(opts.force); err != nil {
		return err
	}

	// 解包到工作目录下的临时目录，而不是系统临时目录：与最终位置同卷，
	// 后面的 rename 才是原子的（媒体目录可能挂在另一块盘上，那种情况由
	// swapIn 退化成拷贝）。
	staging, err := os.MkdirTemp(".", ".restore-")
	if err != nil {
		return fmt.Errorf("create a staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	result, err := reader.Extract(staging, backup.ExtractOptions{})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout(), "Archive verified: %d file(s), %d bytes, checksums match.\n", result.Files, result.Bytes)

	if opts.dryRun {
		fmt.Fprintln(stdout(), "Dry run: nothing was changed.")
		return nil
	}

	if !opts.yes {
		if err := confirmRestore(stdout(), stdin()); err != nil {
			return err
		}
	}

	safetyPath := ""
	if !opts.noSafetyBackup {
		safetyPath, err = createSafetyBackup()
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout(), "Safety backup: %s\n", safetyPath)
	} else {
		fmt.Fprintln(stdout(), "Warning: no safety backup will be taken (--no-safety-backup); "+
			"the data that is replaced now cannot be recovered.")
	}

	if err := applyRestore(staging, manifest, opts); err != nil {
		return err
	}

	logging.Infof("[restore] restored from %s", opts.archive)
	fmt.Fprintln(stdout(), "\nRestore complete.")
	if safetyPath != "" {
		fmt.Fprintf(stdout(), "  roll back with: restore %s\n", safetyPath)
	}
	if manifest.IsPart(backup.PartSearch) {
		// 索引是跟着数据库走的：还原回来的库与旧索引不是一回事，
		// 而启动时的全量同步会把它重新灌对。说一句是免得有人以为索引也还原了。
		fmt.Fprintln(stdout(), "  the search index will be re-synced from the database on the next start")
	}
	return nil
}

// openArchiveSource 打开归档，返回读取器与一个必须被调用的清理函数。
//
// "-" 表示从标准输入读，用于 `ssh host 'exe backup -o -' | exe restore -`
// 这种把归档直接推过网络的用法——它不需要在目标机器上先落一份中间文件。
func openArchiveSource(path string) (io.Reader, func(), error) {
	if path == "-" {
		return stdin(), func() {}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	return file, func() { file.Close() }, nil
}

// reportManifest 打印归档的来历与内容。
//
// 「来历」是有用的：同一台机器上可能有多个站点的归档，而它们的文件名长得差不多。
func reportManifest(w io.Writer, manifest backup.Manifest) {
	fmt.Fprintf(w, "Archive: %s v%d, created %s by %s/%s\n",
		manifest.Format, manifest.FormatVersion,
		manifest.CreatedAt.Format("2006-01-02 15:04:05 MST"),
		manifest.BuiltBy.OS, manifest.BuiltBy.Arch)
	for _, part := range manifest.Parts {
		detail := ""
		if part.Files > 0 {
			detail = fmt.Sprintf("  (%d files)", part.Files)
		}
		fmt.Fprintf(w, "  %-18s %10d bytes%s\n", part.Name, part.Bytes, detail)
	}
}

// refuseIfRunning 在服务仍在运行时挡下还原。
//
// 为什么必须挡：还原会替换 blog.db、删掉它的 -wal/-shm、把整个媒体目录换掉，
// 而正在运行的进程持有旧文件的句柄——它随后写下的东西会落到已经被替换掉的
// inode 上，看起来一切正常，数据却在往回收站外面掉；启动时的索引同步还会把
// 还原回来的库重新灌一遍。这类损坏不报错，只让数据慢慢对不上。
func refuseIfRunning(force bool) error {
	info, running := pidfile.Holder(pidfile.FileName)
	if running {
		if !force {
			return fmt.Errorf("this site is still running (pid %d, started %s): stop it before restoring, "+
				"or pass --force if you are sure that process is not this site",
				info.PID, info.StartedAt.Format("2006-01-02 15:04:05 MST"))
		}
		fmt.Fprintf(stdout(), "Warning: pid %d is still running; continuing because --force was given.\n", info.PID)
		return nil
	}
	if info.PID > 0 {
		// 陈旧锁：进程已经不在，文件没删掉（崩溃过，或者机器直接断电）。
		// 提一句就够——把它当成「在跑」会把用户永久挡在门外。
		fmt.Fprintf(stdout(), "Note: %s is left over from pid %d, which is no longer running.\n", pidfile.FileName, info.PID)
	}
	return nil
}

// confirmRestore 要求操作者亲手打出 yes。
//
// 输入不是终端时（管道、systemd、cron）**直接拒绝**，而不是把「没人能回答」
// 当成同意：这条命令会不可逆地替换整站数据，那是唯一真正危险的默认值。
// 非交互场景请显式传 --yes——那是一个能被审查、能看到是谁加的决定。
func confirmRestore(out io.Writer, in io.Reader) error {
	if !isTerminal(in) {
		return errors.New("stdin is not a terminal: pass --yes to confirm a restore from a script")
	}
	fmt.Fprint(out, "This will REPLACE the current site data. Type 'yes' to continue: ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read the confirmation: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(line)) != "yes" {
		return errors.New("restore cancelled")
	}
	return nil
}

// isTerminal 判断输入是不是一个交互终端。
//
// 它是**变量**而非常规函数：测试无法伪造一个字符设备终端，而「非终端一律拒绝」
// 与「输入不是 yes 就取消」恰恰是最该被钉住的两条分支——它们是「没人能回答不等于
// 同意」这条规则的实现。留一个可替换的钩子，比为了可测性把这些判断塞进一个更大的
// 参数列表要好；与 stdin/stdout/stderr 是同一类做法。
var isTerminal = func(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// createSafetyBackup 在动任何数据之前，给当前状态打一份完整归档。
//
// 它是还原唯一的回滚点：还原会把 blog.db 与媒体目录整个换掉，而归档里装的完全
// 可能是另一个站点的数据（或者在错误的目录里跑了这条命令）。没有它，一次操作
// 失误就是终局。
//
// 复用 performBackup 而不是另写一套：安全备份与普通备份必须是同一种东西，
// 否则「用备份回滚」这条退路本身就需要被单独验证——而它正是最没机会被试到的那条。
func createSafetyBackup() (string, error) {
	name := "twilight-backup-pre-restore-" + nowStamp() + ".tar.gz"
	// force 是有意的：同一秒内跑第二次还原时覆盖上一份安全备份是无害的，
	// 因为那时的「当前状态」已经是第一次还原之后的状态，前一份只剩下更早的
	// 那份历史数据——而那些数据本来就在更早的备份里。
	if _, err := performBackup(backupOptions{output: name, force: true}); err != nil {
		return "", fmt.Errorf("create the safety backup: %w", err)
	}
	return name, nil
}

// revokeRestoredSessions 把还原进来的库里所有会话删掉。
//
// 为什么默认要删：还原会把 sessions 表回滚到备份那一刻，于是**备份之后被吊销的
// token 会复活**。一个已经被踢下线的设备重新拿到有效凭证，是实打实的安全后果，
// 而它发生时没有任何提示——只有 --keep-sessions 才保留这份「精确副本」。
//
// 顺带完成一次 schema 迁移：还原进来的库可能来自旧版本，让它现在就走完升级，
// 好过等到第一次对外服务时才发现它有问题。能成功打开并迁移，本身就是对
// 「这份还原出来的库是好库」的一次验证。
func revokeRestoredSessions() error {
	db, err := database.Open(databasePath)
	if err != nil {
		return fmt.Errorf("open the restored database: %w", err)
	}
	defer db.Close()

	if err := session.NewStore(db).RevokeAll(context.Background()); err != nil {
		return fmt.Errorf("revoke the restored sessions: %w", err)
	}
	return nil
}

// applyRestore 把已经校验过的暂存内容换进工作目录。
//
// 顺序不是随意的：settings.json 先落地决定媒体目录去哪，数据库换位之前必须先
// 删掉 -wal/-shm（理由见 removeSQLiteSidecars）。
//
// 两条与「归档不可信」直接相关的规则：
//
//   - 媒体目录的落点**由本机决定**（本机 settings.json 的 media_dir，或显式的
//     --media-dir），归档里的 media_dir 只用于打印一句提示。此前它被直接当成目标
//     路径，于是一份格式合法的归档就能删除/替换工作目录之外的任意目录（R7-01）；
//   - 归档的 settings.json 在落地**之前**要过一遍安全字段比对，会削弱本机防护的
//     配置默认拒绝（R7-03）。改 media_dir 也在这里完成，保证文件与落点一致。
func applyRestore(staging string, manifest backup.Manifest, opts restoreOptions) error {
	mediaDir, err := restoreMediaDir(opts)
	if err != nil {
		return err
	}

	stagedSettings, err := stagedSettingsPath(staging, manifest)
	if err != nil {
		return err
	}
	restoresSettings := stagedSettings != "" && !opts.keepSettings
	if restoresSettings {
		local, err := readSettingsMap(settingsPath)
		if err != nil {
			return err
		}
		archive, err := readSettingsMap(stagedSettings)
		if err != nil {
			return err
		}
		if err := validateArchiveSettings(archive, local, opts.acceptArchiveSettings); err != nil {
			return err
		}
		// 只在值确实不同时改写：归档与本机布局一致的常见情形下，归档里的字节
		// 原样落地，不做一次无谓的重新编码。
		if archived := stringAt(archive, "media_dir"); archived != mediaDir {
			fmt.Fprintf(stdout(), "Note: the archive's media_dir is %q; media is restored into this host's %q.\n",
				archived, mediaDir)
			if err := setSettingsMediaDir(stagedSettings, mediaDir); err != nil {
				return err
			}
		}
	}

	// 没有安全备份兜底时保留旧数据（重命名而非删除）：删掉它等于把回滚的路堵死。
	keepOld := opts.noSafetyBackup
	var leftovers []string

	swap := func(source, dest, label string) error {
		kept, err := swapIn(source, dest, keepOld)
		if err != nil {
			return fmt.Errorf("restore %s: %w", label, err)
		}
		if kept != "" {
			leftovers = append(leftovers, kept)
		}
		fmt.Fprintf(stdout(), "  restored %s\n", label)
		return nil
	}

	restored := 0
	for _, part := range manifest.Parts {
		rel, err := backup.RelativePath(part.Path)
		if err != nil {
			return err
		}
		source := filepath.Join(staging, rel)

		switch part.Name {
		case backup.PartSettings:
			if opts.keepSettings {
				fmt.Fprintf(stdout(), "  kept this host's %s (--keep-settings)\n", settingsPath)
				continue
			}
			if err := swap(source, settingsPath, settingsPath); err != nil {
				return err
			}
		case backup.PartDatabase:
			// 顺序不能反：陈旧的 WAL 必须在新的 blog.db 落地**之前**消失。
			if err := removeSQLiteSidecars(databasePath); err != nil {
				return err
			}
			if err := swap(source, databasePath, databasePath); err != nil {
				return err
			}
		case backup.PartMedia:
			if err := swap(source, mediaDir, mediaDir); err != nil {
				return err
			}
		case backup.PartSearch:
			if err := swap(source, search.DefaultDataDir, search.DefaultDataDir); err != nil {
				return err
			}
		case backup.PartSearchKey:
			keyPath := search.MasterKeyPath(search.DefaultDataDir)
			if err := swap(source, keyPath, keyPath); err != nil {
				return err
			}
		case backup.PartLogs:
			// 日志是诊断物，不属于运行数据；把它盖回去只会冲掉当前的审计记录，
			// 包括这次还原自己刚刚写下的那些行。
			fmt.Fprintf(stdout(), "  skipped %s (log files are not restored)\n", filepath.Base(rel))
			continue
		default:
			return fmt.Errorf("the archive contains a part this build does not understand: %q (%s)", part.Name, part.Path)
		}
		restored++
	}

	if restored == 0 {
		return errors.New("the archive contained nothing this build knows how to restore")
	}

	if opts.keepSessions {
		fmt.Fprintln(stdout(), "  kept the sessions from the archive (--keep-sessions)")
	} else {
		if err := revokeRestoredSessions(); err != nil {
			return err
		}
		fmt.Fprintln(stdout(), "  revoked every session: every device must sign in again")
	}

	if len(leftovers) > 0 {
		fmt.Fprintln(stdout(), "  the previous data was kept aside:")
		for _, path := range leftovers {
			fmt.Fprintf(stdout(), "    %s\n", path)
		}
	}
	return nil
}
