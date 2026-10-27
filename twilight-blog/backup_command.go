package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"blog-engine/internal/backup"
	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/logging"
	"blog-engine/internal/search"
)

// 本文件实现 `<executable> backup` 子命令。
//
// 它回答的问题是「这个站点里哪些数据丢了就重建不出来」。答案不是「整个目录」：
//
//   - 搜索索引每次启动都会从数据库全量重建（router.go 的 Sync），备份它只是让
//     归档白白变大，还多带一个必须与索引成对的密钥文件；
//   - 日志是诊断物，不是数据；
//   - bin/ 里是下载来的 Meilisearch 可执行文件，与平台绑定，换台机器必然重下。
//
// 真正不可替代的只有三样：blog.db、settings.json、媒体目录。前两者是硬性的
// （少了它们还原出来的不是一个站点），而媒体目录是**硬删、没有回收站**的，
// 磁盘上那份是唯一副本。
const backupCommandUsage = `Usage:
  <executable> backup [-o <file>|-] [--with-index] [--with-logs] [--force] [--quiet]

Writes one tar.gz archive holding everything this site cannot rebuild: the
SQLite database, settings.json and the media directory. The search index and the
log files are left out unless you ask for them.

Options:
  -o, --output <file>   Where to write the archive. "-" writes it to stdout so it
                        can be piped to another machine. Defaults to
                        twilight-backup-<UTC timestamp>.tar.gz
      --with-index      Also archive meilisearch-data/ and its master key. The
                        index is rebuilt from the database on every start, so this
                        only makes the archive self-contained (and bigger).
      --with-logs       Also archive log.txt and its rotated generations.
      --force           Overwrite the output file if it already exists.
      --quiet           Print only the archive path, for use in scripts.

The archive contains every account's password hash and every media file. Treat
it like the database it carries: keep it at 0600 and never commit it.

This command is safe to run while the site is serving: the database is copied
through VACUUM INTO, which takes a consistent snapshot without blocking writes.
`

// backupOptions 是 backup 子命令解析出来的参数。
type backupOptions struct {
	output    string
	withIndex bool
	withLogs  bool
	force     bool
	quiet     bool
}

// runBackupCommand 执行 backup 子命令，返回进程退出码。
var (
	configureBackupLogging = logging.Configure
	backupCreateFn         = backup.Create
	closeArchiveTemp       = func(f *os.File) error { return f.Close() }
	hashArchiveFn          = backup.HashFile
	absPathForBackup       = filepath.Abs
	readBuildInfoFn        = debug.ReadBuildInfo
)

func runBackupCommand(args []string) int {
	opts, err := parseBackupArgs(args)
	if err != nil {
		fmt.Fprintln(stderr(), err)
		return exitUsageOrValidation
	}

	// 与 admin / email 子命令一致：先让日志可用，这样一次备份（以及它引出的那些
	// 警告）会在 log.txt 里留下痕迹。
	if err := configureBackupLogging(""); err != nil {
		fmt.Fprintf(stderr(), "configure logging: %v\n", err)
		return exitFailure
	}

	manifest, err := performBackup(opts)
	if err != nil {
		fmt.Fprintf(stderr(), "backup failed: %v\n", err)
		return exitFailure
	}

	if !opts.quiet {
		// `-o -` 时归档本身占着 stdout，摘要只能改走 stderr，
		// 否则「把归档管到另一个文件里」会连带把这份摘要也写进去。
		destination := stdout()
		if opts.output == "-" {
			destination = stderr()
		}
		reportBackup(destination, opts.output, manifest)
	} else if opts.output != "-" {
		fmt.Fprintln(stdout(), opts.output)
	}
	return 0
}

// parseBackupArgs 解析参数，四种写法（-x V / --x V / -x=V / --x=V）与其它子命令一致。
func parseBackupArgs(args []string) (backupOptions, error) {
	var opts backupOptions

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-o" || arg == "--output":
			if i+1 >= len(args) {
				return opts, errors.New("missing value for -o")
			}
			i++
			if err := setBackupOutput(&opts, args[i]); err != nil {
				return opts, err
			}
		case strings.HasPrefix(arg, "-o="):
			if err := setBackupOutput(&opts, strings.TrimPrefix(arg, "-o=")); err != nil {
				return opts, err
			}
		case strings.HasPrefix(arg, "--output="):
			if err := setBackupOutput(&opts, strings.TrimPrefix(arg, "--output=")); err != nil {
				return opts, err
			}
		case arg == "--with-index":
			opts.withIndex = true
		case arg == "--with-logs":
			opts.withLogs = true
		case arg == "--force":
			opts.force = true
		case arg == "--quiet":
			opts.quiet = true
		default:
			return opts, fmt.Errorf("unknown argument %q\n\n%s", arg, backupCommandUsage)
		}
	}

	if opts.output == "" {
		opts.output = defaultArchiveName(time.Now())
	}
	return opts, nil
}

// setBackupOutput 记下输出目标，并拒绝重复指定——两个 -o 谁赢都像是「照做了」，
// 但用户看到的路径很可能是被覆盖掉的那一个。
func setBackupOutput(opts *backupOptions, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("the output path must not be empty")
	}
	if opts.output != "" {
		return errors.New("-o/--output specified more than once")
	}
	opts.output = value
	return nil
}

// defaultArchiveName 用 UTC 时间戳命名。
//
// UTC 而不是本地时间：排障时要把归档和日志里的时间对上，而「这是几点打的」
// 在不同时区的机器上有不同答案——这种歧义最费时间。
func defaultArchiveName(now time.Time) string {
	return "twilight-backup-" + now.UTC().Format("20060102-150405") + ".tar.gz"
}

// performBackup 组装数据源、写归档，返回清单。
//
// 它同时被 `restore` 复用去打「还原前安全备份」：两者必须是同一种东西，
// 否则「用备份回滚」这条退路本身就需要被单独验证一遍。
func performBackup(opts backupOptions) (*backup.Manifest, error) {
	// settings.json 与 blog.db 必须先存在。缺了它们的目录不是一个「已安装的
	// 站点」，在这里失败比产出一份没有配置的归档好——后者要到还原那天才暴露。
	for _, required := range []string{settingsPath, databasePath} {
		if _, err := os.Stat(required); err != nil {
			return nil, fmt.Errorf("%s not found: run this from the site's working directory", required)
		}
	}

	mediaDir := readMediaDir(settingsPath)
	if err := checkOutputTarget(opts.output, mediaDir); err != nil {
		return nil, err
	}

	// 数据库先做一致性快照再进归档（理由见 database.Snapshot）。快照放系统临时
	// 目录：它只是中转，归档写完就删，不该在工作目录里留下痕迹。
	snapshotDir, err := os.MkdirTemp("", "twilight-backup-")
	if err != nil {
		return nil, fmt.Errorf("create a snapshot directory: %w", err)
	}
	defer os.RemoveAll(snapshotDir)

	snapshot := filepath.Join(snapshotDir, databasePath)
	if err := database.Snapshot(databasePath, snapshot); err != nil {
		return nil, err
	}

	entries := []backup.Entry{
		{Name: backup.PartDatabase, Source: snapshot, Target: backup.DataPrefix + databasePath, Hash: true},
		{Name: backup.PartSettings, Source: settingsPath, Target: backup.DataPrefix + settingsPath, Hash: true},
	}

	// 媒体目录可能还不存在（全新站点、一张图都没传过）。那不是错误，
	// 静默跳过即可——但要知道自己在跳过什么，所以下面会把它报出来。
	if info, err := os.Stat(mediaDir); err == nil && info.IsDir() {
		entries = append(entries, backup.Entry{
			Name:   backup.PartMedia,
			Source: mediaDir,
			Target: backup.DataPrefix + filepath.ToSlash(filepath.Base(mediaDir)),
		})
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect the media directory %s: %w", mediaDir, err)
	}

	if opts.withIndex {
		// 索引与密钥必须成对：密钥被记进 Meilisearch 自己的鉴权库，换一把钥匙的
		// 旧库对不上。少了任何一个，带走的索引都只是一堆读不出来的文件。
		if info, err := os.Stat(search.DefaultDataDir); err == nil && info.IsDir() {
			entries = append(entries, backup.Entry{
				Name:   backup.PartSearch,
				Source: search.DefaultDataDir,
				Target: backup.DataPrefix + filepath.ToSlash(filepath.Base(search.DefaultDataDir)),
			})
		}
		keyPath := search.MasterKeyPath(search.DefaultDataDir)
		if info, err := os.Stat(keyPath); err == nil && info.Mode().IsRegular() {
			entries = append(entries, backup.Entry{
				Name:   backup.PartSearchKey,
				Source: keyPath,
				Target: backup.DataPrefix + filepath.ToSlash(filepath.Base(keyPath)),
			})
		}
	}

	if opts.withLogs {
		// 轮转过的日志（log.txt.1 …）也要带上：只带当前那一个，最老的那批
		// 恰恰是「出问题之前」的记录。
		// 模式是固定字面量，Glob 不会因为调用方输入而失败；不再写一个
		// 永远不可达的错误分支。
		matches, _ := filepath.Glob(logging.DefaultLogFile + "*")
		sort.Strings(matches)
		for _, path := range matches {
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			entries = append(entries, backup.Entry{
				Name:   backup.PartLogs,
				Source: path,
				Target: backup.DataPrefix + filepath.ToSlash(filepath.Base(path)),
			})
		}
	}

	builtBy := backup.BuiltBy{OS: runtime.GOOS, Arch: runtime.GOARCH, Revision: buildRevision()}
	return writeArchive(opts, entries, builtBy)
}

// writeArchive 把 entries 写到 opts.output。
//
// 文件输出走「同目录临时文件 + rename」。直接写目标路径时，中途失败（磁盘写满、
// 进程被杀）会留下一个半截的 .tar.gz，而它看上去和一份完整归档没有区别——
// 直到有人真的拿它去还原。rename 之后目标路径要么是完整的，要么根本不存在。
func writeArchive(opts backupOptions, entries []backup.Entry, builtBy backup.BuiltBy) (*backup.Manifest, error) {
	if opts.output == "-" {
		return backup.Create(stdout(), entries, builtBy, time.Now())
	}

	// 先查再建：默认拒绝覆盖是有意的，一份三个月前的归档比一个手滑更值钱。
	if !opts.force {
		if _, err := os.Stat(opts.output); err == nil {
			return nil, fmt.Errorf("%s already exists: pass --force to overwrite it", opts.output)
		}
	}

	dir := filepath.Dir(opts.output)
	temp, err := os.CreateTemp(dir, ".twilight-backup-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create the archive in %s: %w", dir, err)
	}
	tempName := temp.Name()
	// 任何提前返回都要清掉临时文件，否则备份失败会在目标目录里留下一个
	// 看不懂的东西。成功时把 tempName 清空，这条 defer 就成了空操作。
	defer func() {
		if tempName != "" {
			os.Remove(tempName)
		}
	}()

	manifest, createErr := backupCreateFn(temp, entries, builtBy, time.Now())
	closeErr := closeArchiveTemp(temp)
	if createErr != nil {
		return nil, createErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("finish the archive: %w", closeErr)
	}

	if err := os.Rename(tempName, opts.output); err != nil {
		return nil, fmt.Errorf("move the archive into place: %w", err)
	}
	tempName = ""

	// 旁文件记下整卷的 sha256。传输之后核对一次是很便宜的保险，
	// 而一份没被校验过的备份只能算「很可能完整」。
	sum, err := hashArchiveFn(opts.output)
	if err != nil {
		logging.Warnf("[backup] could not hash the new archive: %v", err)
		return manifest, nil
	}
	sidecar := opts.output + ".sha256"
	if err := os.WriteFile(sidecar, []byte(sum+"  "+filepath.Base(opts.output)+"\n"), 0o600); err != nil {
		logging.Warnf("[backup] could not write %s: %v", sidecar, err)
	}
	return manifest, nil
}

// reportBackup 打印归档的内容清单。
//
// 逐项列出体积是刻意的：扫一眼就能发现「这次媒体怎么只有 3 个文件」，
// 而不是等到需要还原的那天才知道少了什么。
func reportBackup(w io.Writer, output string, manifest *backup.Manifest) {
	if output == "-" {
		output = "(stdout)"
	}
	fmt.Fprintf(w, "Archive written to %s\n", output)
	fmt.Fprintf(w, "  created %s\n", manifest.CreatedAt.Format(time.RFC3339))

	var total int64
	for _, part := range manifest.Parts {
		detail := ""
		if part.Files > 0 {
			detail = fmt.Sprintf("  (%d files)", part.Files)
		}
		fmt.Fprintf(w, "  %-18s %10d bytes%s\n", part.Name, part.Bytes, detail)
		total += part.Bytes
	}
	fmt.Fprintf(w, "  %-18s %10d bytes\n", "total", total)
}

// checkOutputTarget 拒绝把归档写进媒体目录。
//
// 归档是流式写的，而媒体目录正是它自己正在读的那棵树：写进去等于一边读一边改
// 被读的内容（新文件会被 WalkDir 走到，甚至被自己包含进去）。而更常见的原因是
// 它几乎总是笔误。
func checkOutputTarget(output, mediaDir string) error {
	if output == "" || output == "-" {
		return nil
	}
	absOutput, err := absPathForBackup(output)
	if err != nil {
		return nil // 规范化不了就交给后面写文件时报错，那里有更具体的上下文
	}
	absMedia, err := absPathForBackup(mediaDir)
	if err != nil {
		return nil
	}
	if within(absMedia, absOutput) {
		return fmt.Errorf("refusing to write the archive into the media directory (%s): choose a path outside it", mediaDir)
	}
	return nil
}

// within 报告 path 是否落在 dir 之内（含 dir 自身）。
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

// readMediaDir 读出 settings.json 里的 media_dir，缺失或未配置时回退到默认值。
func readMediaDir(path string) string {
	return readSiteSetting(path, "media_dir", config.DefaultMediaDir)
}

// readSiteSetting 读出 settings.json 里一个字符串字段；文件缺失、无法解析、该键
// 不存在或只是空白时，一律返回 fallback。
//
// 刻意不走 config.Load：那个函数会做迁移、可能回写文件，而这里只是想**读一个
// 字符串**。为了一个路径去触发一次「就地升级」，是照着「反正有现成函数」写下的
// 代码——它会带来难以解释的副作用（例如在一个只读目录里跑备份或升级命令）。
//
// 与 config.Load 一样容忍 UTF-8 BOM：Windows 编辑器常加，
// 而 encoding/json 遇到它会直接报 invalid character。
func readSiteSetting(path, key, fallback string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	var settings map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), &settings); err != nil {
		return fallback
	}
	// 类型断言而不是直接取值：配错类型（例如把 webp_tool 写成数字）应当退到 fallback，
	// 让上层走「没配」那条路，而不是把 fmt 出来的数字当成路径去 exec。
	value, _ := settings[key].(string)
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

// buildRevision 从二进制里读出 VCS 修订号，写进归档清单。
//
// 它读的是 `go build` 自动嵌进二进制的 build info（Go 1.18 起默认开启），
// 不需要构建脚本额外传 -ldflags。从 VCS 之外的目录、或用 -buildvcs=false 构建时
// 读不到值，此时如实留空——**不要编一个假的**：一个错的版本号会让排障方向完全跑偏。
func buildRevision() string {
	info, ok := readBuildInfoFn()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}
