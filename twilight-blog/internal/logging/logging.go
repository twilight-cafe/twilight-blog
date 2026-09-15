// Package logging 提供全进程共用的日志：级别过滤、按大小轮转、同时写文件与 stderr。
//
// 三个刻意的性质：
//   - **绝不因为日志本身出问题而丢日志或中断请求**：文件打不开就退回 stderr，
//     写入失败也先把字节交给 stderr，再返回错误。
//   - 日志固定写到工作目录下的 log.txt，权限 0600，并且是**追加**而不是覆盖，
//     所以重启不会抹掉崩溃之前发生过什么的记录。
//   - **每条日志恰好占一行，且不含控制字符**（见 sanitizeLogLine）。
//     这一条是安全性质而不是排版偏好：日志里有大量调用方直接把请求数据
//     （URL 路径、查询串、错误文本）塞进 %s，而 Go 的 net/url 会把 %0A 解码成
//     真实换行。不转义的话，任何未认证的调用方都能往 log.txt 里写入与真实
//     日志逐字节同构的伪造行（例如一条假的 [FATAL] 或假的来源 IP），
//     从而污染检测与取证。
package logging

import (
	"fmt"
	"io"
	stdlog "log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// DefaultLogFile 是日志文件的固定名称（相对于进程的工作目录）。
const DefaultLogFile = "log.txt"

// DefaultMaxLogSize 是触发轮转的文件大小上限。
// 没有上限时，一个话痨的 debug 配置最终会把磁盘写满。
const DefaultMaxLogSize = 10 << 20 // 10 MiB

// maxLogBackups 是保留的历史代数（log.txt.1 .. log.txt.N），
// 也就是最坏情况下日志最多占用 (N+1) 份空间。
const maxLogBackups = 3

// Level 控制哪些日志会被输出，数值越大越严重。
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// 当前级别与已打开的日志文件是进程级状态，会被大量 goroutine 读取
// （每个请求都要写日志），因此两者都做了保护：级别用原子量，
// 文件及其已写长度用互斥锁。
var (
	level     atomic.Int32
	logMu     sync.Mutex
	logFile   *os.File
	loggedLen int64
)

// init 把默认级别设为 Info，并让标准库 log 也走本包的轮转 Writer，
// 这样第三方组件（echo 的 StdLogger 等）的输出同样会进日志文件。
func init() {
	level.Store(int32(LevelInfo))
	stdlog.SetFlags(stdlog.LstdFlags)
	stdlog.SetOutput(Writer())
}

// ParseLevel 把 settings.json 里的 log_level 文本转成 Level，空值表示默认的 normal。
// 接受多组别名（dev/debug、normal/info、warn/warning、error/quiet）。
// 无法识别时返回错误而不是回退——一个拼错的级别不该静默变成别的级别。
func ParseLevel(value string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return LevelInfo, nil
	case "dev", "debug":
		return LevelDebug, nil
	case "normal", "info":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error", "quiet":
		return LevelError, nil
	default:
		return LevelInfo, fmt.Errorf("unsupported log level %q", value)
	}
}

// SetLevel 更新全局日志阈值。
func SetLevel(l Level) {
	level.Store(int32(l))
}

// Configure 解析并应用日志级别，然后打开日志文件。
//
// 顺序很重要：级别必须先生效，否则「解析级别失败」这条错误自己就会被过滤掉。
func Configure(value string) error {
	parsed, err := ParseLevel(value)
	if err != nil {
		return err
	}
	SetLevel(parsed)
	return ensureLogFile()
}

// CurrentLevel 返回当前级别。
func CurrentLevel() Level {
	return Level(level.Load())
}

// String 返回该级别在 settings.json 里的规范写法（即 ParseLevel 接受的首选别名）。
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "dev"
	case LevelInfo:
		return "normal"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "normal"
	}
}

// Writer 返回日志输出端：同时写日志文件与 stderr，按大小轮转，可并发使用。
func Writer() io.Writer {
	return rotatingWriter{}
}

// rotatingWriter 把日志追加写入文件，并把同样的字节镜像到 stderr。
// 它是零值可用的空结构体，状态都在包级变量里。
type rotatingWriter struct{}

// Write 实现 io.Writer：先确保文件已打开，超限则先轮转，
// 然后写文件并镜像到 stderr。
// 前提：内部自行加锁，调用方无需持锁。
func (rotatingWriter) Write(p []byte) (int, error) {
	logMu.Lock()
	defer logMu.Unlock()

	if err := openLocked(); err != nil {
		// 不能因为文件不可用就整行丢掉——退回 stderr 至少还有人看得见。
		return os.Stderr.Write(p)
	}
	if loggedLen+int64(len(p)) > DefaultMaxLogSize {
		rotateLocked()
	}

	n, err := logFile.Write(p)
	loggedLen += int64(n)

	if _, stderrErr := os.Stderr.Write(p); stderrErr != nil && err == nil {
		err = stderrErr
	}
	return n, err
}

// ensureLogFile 确保日志文件已打开（幂等）。
func ensureLogFile() error {
	logMu.Lock()
	defer logMu.Unlock()
	return openLocked()
}

// openLocked 在日志文件尚未打开时打开它（幂等）。调用方必须持有 logMu。
//
// 采用**追加**而不是截断：重启不会抹掉崩溃之前发生过什么的记录。
// 权限 0600，因为日志里可能含文件路径、邮箱域名等信息。
func openLocked() error {
	if logFile != nil {
		return nil
	}

	f, err := os.OpenFile(DefaultLogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}

	logFile = f
	loggedLen = info.Size()
	return nil
}

// rotateLocked 做一轮轮转：log.txt.1 → .2 → … 逐级后移，丢弃最老的一代，
// 然后重新打开一个新的空 log.txt。调用方必须持有 logMu。
//
// 过程中的失败只写到 stderr，不能走日志系统本身——那会递归回这里。
func rotateLocked() {
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}

	for i := maxLogBackups - 1; i >= 1; i-- {
		oldName := fmt.Sprintf("%s.%d", DefaultLogFile, i)
		newName := fmt.Sprintf("%s.%d", DefaultLogFile, i+1)
		if err := os.Rename(oldName, newName); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "log rotation: rename %s: %v\n", oldName, err)
		}
	}
	if err := os.Rename(DefaultLogFile, DefaultLogFile+".1"); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "log rotation: rename %s: %v\n", DefaultLogFile, err)
	}

	loggedLen = 0
	if err := openLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "log rotation: reopen failed: %v\n", err)
	}
}

func Debugf(format string, args ...any) {
	logf(LevelDebug, format, args...)
}

func Infof(format string, args ...any) {
	logf(LevelInfo, format, args...)
}

func Warnf(format string, args ...any) {
	logf(LevelWarn, format, args...)
}

func Errorf(format string, args ...any) {
	logf(LevelError, format, args...)
}

// Fatalf 记录后直接退出进程（非零状态），仅用于不可恢复的**启动期**故障。
//
// 注意它走的是标准库 log.Fatalf，因此会跳过所有 defer——
// 调用前需要清理的资源必须显式清理。
func Fatalf(format string, args ...any) {
	stdlog.Fatalf("%s", sanitizeLogLine("[FATAL] "+fmt.Sprintf(format, args...)))
}

// logf 是各个级别的公共实现：低于当前级别直接丢弃，
// 否则交给标准库 log 输出（其输出端已指向本包的轮转 Writer）。
//
// 先 Sprintf 再由 sanitizeLogLine 压成单行，最后以 %s 交给标准库——
// 也就是说调用方传入的格式化结果永远只被当作**数据**，不会被二次解释。
func logf(l Level, format string, args ...any) {
	if int32(l) < level.Load() {
		return
	}
	stdlog.Printf("%s", sanitizeLogLine(label(l)+" "+fmt.Sprintf(format, args...)))
}

// maxLogLineBytes 是单条日志行的字节上限。
//
// 存在的理由：调用方会把请求数据直接塞进日志（当前最长的是搜索关键词，
// 实测 20 000 字节仍被接受）。没有上限时，一个请求就能往 log.txt 里灌入
// 任意长度的内容，配合 10 MiB 的轮转阈值把真实记录快速挤出保留窗口——
// 那是一种「用日志把证据冲掉」的破坏手法，成本极低。
//
// 取 8192：远大于任何正常的单行日志（当前最长者约 1 KiB），
// 又远小于轮转阈值，因此既不会误伤，也让灌日志的性价比变得很差。
const maxLogLineBytes = 8192

// sanitizeLogLine 把一条日志消息压成**单行且不含控制字符**的形式。
//
// 这是本包的安全不变量，不要因为「日志看起来更整齐」之外的任何理由删掉它；
// 更要紧的是不要把它挪到 rotatingWriter.Write 里去——那里的输入还包括
// 标准库与第三方组件的输出（panic 的堆栈就是多行的），压平堆栈会毁掉
// 排障能力。真正需要防的是「调用方把用户数据拼进日志」，而那正是 logf 的入口。
//
// 转换规则：
//   - \n \r \t 变成可见的 \n \r \t 两个字符（保留信息，便于看出有人试图注入多行）；
//   - 其余 C0 控制字符与 DEL 变成 \xNN；
//   - 超过 maxLogLineBytes 时按 UTF-8 边界截断并标注丢弃的字节数。
func sanitizeLogLine(message string) string {
	if message == "" {
		return message
	}

	var b strings.Builder
	b.Grow(len(message) + 8)

	for _, r := range message {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}

	out := b.String()
	if len(out) <= maxLogLineBytes {
		return out
	}

	// 回退到 UTF-8 边界，避免把一个多字节字符切成两半。
	cut := maxLogLineBytes
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + fmt.Sprintf("...[truncated %d bytes]", len(out)-cut)
}

// label 返回日志行前缀。四个级别各有固定的中括号标签，
// 便于用 grep 在 log.txt 里按级别筛选。
func label(l Level) string {
	switch l {
	case LevelDebug:
		return "[DEBUG]"
	case LevelInfo:
		return "[INFO]"
	case LevelWarn:
		return "[WARN]"
	case LevelError:
		return "[ERROR]"
	default:
		return "[LOG]"
	}
}

// RedactEmail 保留域名、抹掉本地部分，用于任何会记录邮箱的日志。
//
// 存在的理由：日志文件是**长期累积**的，而注册、找回密码、评论验证码这几条
// 路径上到处都在打邮箱。不脱敏的话 log.txt 会慢慢变成一份「谁来过这个站点」
// 的清单——一份不需要任何权限就能读、还常常被顺手打包进问题反馈里的清单。
// 保留域名对排查投递问题仍然有用（能看出是不是某个域收不到）。
//
// 放在 logging 而不是 mail 包里：这是日志卫生规则，不是邮件逻辑；
// 而且 mail、service 两边都要用它，放在底层才不会出现两份实现各写各的
// （这个仓库此前正是如此：mail 脱敏，service 却打完整地址）。
func RedactEmail(address string) string {
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return "***"
	}
	return "***@" + address[at+1:]
}
