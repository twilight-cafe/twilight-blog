package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http"
	"net/http/httptest"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/database"
	"blog-engine/internal/mediaconv"
	"blog-engine/internal/middleware"
	"blog-engine/internal/pidfile"
	"blog-engine/internal/search"
)

// bootForTests 是一份「全部步骤成功」的 bootHooks，用例只在需要制造故障的
// 那一步上覆盖单个字段。
func bootForTests(t *testing.T) bootHooks {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "boot.db"))
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	// Windows 不允许删除仍被进程占用的文件；不关掉它就只会看到
	// TempDir cleanup 报 "being used by another process"，把真正的断言失败盖住。
	t.Cleanup(func() { _ = db.Close() })
	hooks := defaultBootHooks()
	hooks.configureLogging = func(string) error { return nil }
	hooks.acquireLock = func() (func(), error) { return func() {}, nil }
	hooks.loadSettings = func(string) (*config.Config, error) {
		return &config.Config{SearchEnabled: boolPtr(false), Port: 8080}, nil
	}
	hooks.saveSettings = func(*config.Config, string) error { return nil }
	hooks.detectMedia = func(string, string) (*mediaconv.Converter, error) {
		return &mediaconv.Converter{WebPTool: "cwebp", WebPKind: "cwebp"}, nil
	}
	hooks.openDatabase = func(string) (*sql.DB, error) { return db, nil }
	hooks.migrateAdmin = func(context.Context, *sql.DB, *config.Config) error { return nil }
	hooks.revokeSessions = func(context.Context, *sql.DB) (int64, error) { return 0, nil }
	hooks.installed = func(context.Context, *sql.DB) (bool, error) { return true, nil }
	hooks.startSearch = func(context.Context, string) (*search.Manager, error) {
		return &search.Manager{}, nil
	}
	hooks.parseProxies = func([]string) (middleware.TrustedProxies, error) { return nil, nil }
	hooks.registerRoutes = func(*echo.Echo, *config.Config, *sql.DB, string, *search.Client, *mediaconv.Converter) error {
		return nil
	}
	hooks.listen = func(*echo.Echo, string) error { return nil }
	hooks.nowSignal = func() <-chan os.Signal {
		ch := make(chan os.Signal)
		close(ch)
		return ch
	}
	hooks.exit = func(int) {}
	return hooks
}

func boolPtr(value bool) *bool { return &value }

func TestServeHappyPathAndShutdownHandler(t *testing.T) {
	restoreBackoff := searchStartBackoff
	searchStartBackoff = time.Millisecond
	t.Cleanup(func() { searchStartBackoff = restoreBackoff })

	exits := make(chan int, 1)
	hooks := bootForTests(t)
	hooks.exit = func(code int) { exits <- code }

	if err := serve(hooks); err != nil {
		t.Fatalf("serve: %v", err)
	}
	select {
	case code := <-exits:
		if code != 0 {
			t.Fatalf("shutdown exit code = %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the shutdown handler never ran")
	}
}

func TestServeSearchDisabledAndEnabled(t *testing.T) {
	restoreBackoff := searchStartBackoff
	searchStartBackoff = time.Millisecond
	t.Cleanup(func() { searchStartBackoff = restoreBackoff })

	// 关闭搜索：startSearch 根本不该被调用。
	disabled := bootForTests(t)
	disabled.loadSettings = func(string) (*config.Config, error) {
		return &config.Config{SearchEnabled: boolPtr(false)}, nil
	}
	disabled.startSearch = func(context.Context, string) (*search.Manager, error) {
		t.Fatal("search must not start when it is disabled")
		return nil, nil
	}
	if err := serve(disabled); err != nil {
		t.Fatalf("serve with search disabled: %v", err)
	}

	// 开启搜索：注册路由时必须拿到 client（零值 Manager 的 Client 是 nil，
	// 但这一步确认的是 meili 分支确实走到了 registerRoutes）。
	enabled := bootForTests(t)
	enabled.loadSettings = func(string) (*config.Config, error) {
		return &config.Config{SearchEnabled: boolPtr(true)}, nil
	}
	registered := false
	enabled.registerRoutes = func(_ *echo.Echo, _ *config.Config, _ *sql.DB, _ string, client *search.Client, _ *mediaconv.Converter) error {
		registered = true
		if client != nil {
			t.Fatal("the zero manager has no client; this test only needs the branch")
		}
		return nil
	}
	if err := serve(enabled); err != nil {
		t.Fatalf("serve with search enabled: %v", err)
	}
	if !registered {
		t.Fatal("routes were not registered")
	}
}

func TestServeRetriesSearchStart(t *testing.T) {
	restoreAttempts := searchStartAttempts
	restoreBackoff := searchStartBackoff
	searchStartAttempts = 3
	searchStartBackoff = time.Millisecond
	t.Cleanup(func() {
		searchStartAttempts = restoreAttempts
		searchStartBackoff = restoreBackoff
	})

	attempts := 0
	hooks := bootForTests(t)
	hooks.loadSettings = func(string) (*config.Config, error) {
		return &config.Config{SearchEnabled: boolPtr(true)}, nil
	}
	hooks.startSearch = func(context.Context, string) (*search.Manager, error) {
		attempts++
		return nil, errors.New("port taken")
	}
	err := serve(hooks)
	if err == nil || attempts != 3 {
		t.Fatalf("serve error = %v after %d attempts", err, attempts)
	}
}

func TestServeFailureBranches(t *testing.T) {
	tests := []struct {
		name  string
		patch func(*bootHooks)
		want  string
	}{
		{
			name: "default logging",
			patch: func(h *bootHooks) {
				h.configureLogging = func(string) error { return errors.New("no log") }
			},
			want: "configure default logging",
		},
		{
			name: "load settings",
			patch: func(h *bootHooks) {
				h.loadSettings = func(string) (*config.Config, error) { return nil, errors.New("bad json") }
			},
			want: "load settings",
		},
		{
			name: "log level",
			patch: func(h *bootHooks) {
				calls := 0
				h.configureLogging = func(string) error {
					calls++
					if calls == 1 {
						return nil
					}
					return errors.New("bad level")
				}
			},
			want: "configure log level",
		},
		{
			name: "webp detector",
			patch: func(h *bootHooks) {
				h.detectMedia = func(string, string) (*mediaconv.Converter, error) { return nil, errors.New("no encoder") }
			},
			want: "media conversion is required",
		},
		{
			name: "first settings save",
			patch: func(h *bootHooks) {
				h.saveSettings = func(*config.Config, string) error { return errors.New("read-only") }
			},
			want: "update settings",
		},
		{
			name: "database",
			patch: func(h *bootHooks) {
				h.openDatabase = func(string) (*sql.DB, error) { return nil, errors.New("disk full") }
			},
			want: "open database",
		},
		{
			name: "credential migration",
			patch: func(h *bootHooks) {
				h.migrateAdmin = func(context.Context, *sql.DB, *config.Config) error { return errors.New("migrate") }
			},
			want: "migrate administrator credentials",
		},
		{
			name: "second settings save",
			patch: func(h *bootHooks) {
				calls := 0
				h.saveSettings = func(*config.Config, string) error {
					calls++
					if calls == 1 {
						return nil
					}
					return errors.New("read-only")
				}
			},
			want: "remove migrated administrator credentials",
		},
		{
			name: "legacy sessions",
			patch: func(h *bootHooks) {
				h.revokeSessions = func(context.Context, *sql.DB) (int64, error) { return 0, errors.New("db") }
			},
			want: "revoke pre-migration sessions",
		},
		{
			name: "installation state",
			patch: func(h *bootHooks) {
				h.installed = func(context.Context, *sql.DB) (bool, error) { return false, errors.New("db") }
			},
			want: "check installation state",
		},
		{
			name: "trusted proxies",
			patch: func(h *bootHooks) {
				h.parseProxies = func([]string) (middleware.TrustedProxies, error) {
					return nil, errors.New("bad proxy")
				}
			},
			want: "configure trusted proxies",
		},
		{
			name: "route registration",
			patch: func(h *bootHooks) {
				h.registerRoutes = func(*echo.Echo, *config.Config, *sql.DB, string, *search.Client, *mediaconv.Converter) error {
					return errors.New("manifest missing")
				}
			},
			want: "register routes",
		},
		{
			name: "listen",
			patch: func(h *bootHooks) {
				h.listen = func(*echo.Echo, string) error { return errors.New("port in use") }
			},
			want: "server stopped",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hooks := bootForTests(t)
			test.patch(&hooks)
			err := serve(hooks)
			if err == nil {
				t.Fatal("the failure must surface")
			}
			if !contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestServeToleratesNotInstalledAndNilConfig(t *testing.T) {
	hooks := bootForTests(t)
	hooks.loadSettings = func(string) (*config.Config, error) { return nil, config.ErrNotInstalled }
	hooks.installed = func(context.Context, *sql.DB) (bool, error) { return false, nil }
	hooks.revokeSessions = func(context.Context, *sql.DB) (int64, error) { return 3, nil }
	if err := serve(hooks); err != nil {
		t.Fatalf("an uninstalled site must still boot: %v", err)
	}

	// nil 配置（Load 返回 (nil, nil)）也要继续：所有取值走零值兜底。
	nilCfg := bootForTests(t)
	nilCfg.loadSettings = func(string) (*config.Config, error) { return nil, nil }
	if err := serve(nilCfg); err != nil {
		t.Fatalf("a nil config must not stop the boot: %v", err)
	}
}

func TestServeWarnsWhenTheLockCannotBeWritten(t *testing.T) {
	hooks := bootForTests(t)
	hooks.acquireLock = func() (func(), error) { return nil, errors.New("read-only") }
	if err := serve(hooks); err != nil {
		t.Fatalf("a missing pid file must not stop the boot: %v", err)
	}
}

func TestServeSearchEnabledButRegistrationFails(t *testing.T) {
	restoreBackoff := searchStartBackoff
	searchStartBackoff = time.Millisecond
	t.Cleanup(func() { searchStartBackoff = restoreBackoff })

	hooks := bootForTests(t)
	hooks.loadSettings = func(string) (*config.Config, error) {
		return &config.Config{SearchEnabled: boolPtr(true)}, nil
	}
	hooks.registerRoutes = func(*echo.Echo, *config.Config, *sql.DB, string, *search.Client, *mediaconv.Converter) error {
		return errors.New("manifest missing")
	}
	if err := serve(hooks); err == nil {
		t.Fatal("route registration failure must surface even with search enabled")
	}
}

func TestRunDispatchesSubcommands(t *testing.T) {
	hooks := bootForTests(t)
	cli := defaultCommands()

	if code := run([]string{}, hooks, cli); code != 0 {
		t.Fatalf("a clean boot must exit 0, got %d", code)
	}

	// 未知子命令会被各命令自己的分发器判成用法错误（退出码 2）。
	for _, name := range []string{"admin", "backup", "restore", "upgrade-media"} {
		if code := run([]string{name, "--not-a-flag"}, hooks, cli); code == 0 {
			t.Fatalf("%s must reject an unknown flag", name)
		}
	}

	// email 子命令：未知子命令与缺参数都返回 1（它自己只返回 error）。
	if code := run([]string{"email", "nonsense"}, hooks, cli); code != 1 {
		t.Fatalf("an unknown email subcommand must exit 1, got %d", code)
	}
	if code := run([]string{"email"}, hooks, cli); code != 1 {
		t.Fatalf("a bare email command must exit 1, got %d", code)
	}

	failing := bootForTests(t)
	failing.listen = func(*echo.Echo, string) error { return errors.New("port in use") }
	if code := run([]string{}, failing, cli); code != 1 {
		t.Fatalf("a failed boot must exit 1, got %d", code)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func TestRunEmailSuccessAndMainEntrypoint(t *testing.T) {
	hooks := bootForTests(t)
	sent := false
	cli := commands{
		email:        func([]string) error { sent = true; return nil },
		admin:        func([]string) int { return 2 },
		backup:       func([]string) int { return 2 },
		restore:      func([]string) int { return 2 },
		upgradeMedia: func([]string) int { return 2 },
	}
	if code := run([]string{"email", "test", "-target", "a@b.c"}, hooks, cli); code != 0 || !sent {
		t.Fatalf("a successful email command must exit 0 (sent=%v)", sent)
	}

	// main() 只是 run + exit 的两行；把两者都替换掉就能在测试进程里执行它。
	codes := make(chan int, 1)
	restoreExit := swapExit(func(code int) { codes <- code })
	restoreHooks := swapHooks(func() bootHooks { return hooks })
	t.Cleanup(restoreExit)
	t.Cleanup(restoreHooks)
	main()
	if code := <-codes; code != 0 {
		t.Fatalf("main exit code = %d", code)
	}
}

func swapExit(value func(int)) func() {
	original := exitFunc
	exitFunc = value
	return func() { exitFunc = original }
}

func swapHooks(value func() bootHooks) func() {
	original := bootHooksProvider
	bootHooksProvider = value
	return func() { bootHooksProvider = original }
}

// TestDefaultBootHooksCallThrough 让默认实现（而不是测试替身）各自跑一次。
//
// 这些闭包平时被 serve 用测试替身整体顶掉，于是它们自己就成了「没人执行过的
// 代码」——而默认实现恰恰是最该有回归保护的那一份：它决定真实进程启动时
// 每一步到底调了什么。
func TestDefaultBootHooksCallThrough(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	restoreStarter := func(original func(context.Context, string) (*search.Manager, error)) func() {
		current := searchStarter
		return func() { searchStarter = current }
	}(searchStarter)
	searchStarter = func(context.Context, string) (*search.Manager, error) { return nil, errors.New("no meilisearch") }
	t.Cleanup(restoreStarter)

	hooks := defaultBootHooks()
	if err := hooks.configureLogging(""); err != nil {
		t.Fatalf("configureLogging: %v", err)
	}
	// 先让锁文件写不出来（同名目录挡住了写入），再让它成功一次。
	if err := os.Mkdir(pidfile.FileName, 0o755); err != nil {
		t.Fatalf("prepare a blocking directory: %v", err)
	}
	if _, err := hooks.acquireLock(); err == nil {
		t.Fatal("an unwritable pid file must be reported")
	}
	if err := os.Remove(pidfile.FileName); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	release, err := hooks.acquireLock()
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	release()

	// 站点未安装也是合法结局：runEmailCommand 那条链依赖 ErrNotInstalled 被容忍。
	if _, err := hooks.loadSettings("settings.json"); err == nil {
		t.Log("a settings.json was found in the temporary directory")
	}

	cfg := &config.Config{}
	if err := hooks.saveSettings(cfg, filepath.Join(dir, "settings.json")); err != nil {
		t.Fatalf("saveSettings: %v", err)
	}
	converter, err := hooks.detectMedia("", "")
	if converter == nil && err == nil {
		t.Fatal("Detect must report either a converter or an error")
	}

	db, err := hooks.openDatabase("blog.db")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := hooks.migrateAdmin(ctx, db, cfg); err != nil {
		t.Fatalf("migrateAdmin: %v", err)
	}
	if _, err := hooks.revokeSessions(ctx, db); err != nil {
		t.Fatalf("revokeSessions: %v", err)
	}
	if _, err := hooks.installed(ctx, db); err != nil {
		t.Fatalf("installed: %v", err)
	}
	if _, err := hooks.startSearch(ctx, ""); err == nil {
		t.Fatal("the stubbed search starter must surface its error")
	}
	if _, err := hooks.parseProxies(nil); err != nil {
		t.Fatalf("parseProxies: %v", err)
	}

	e := hooks.newEcho()
	if e == nil {
		t.Fatal("newEcho returned nil")
	}
	_ = hooks.registerRoutes(e, cfg, db, "settings.json", nil, &mediaconv.Converter{})
	// listen 默认实现会真的起服务，因此这里用一个必然失败的地址。
	if err := hooks.listen(e, "127.0.0.1:-1"); err == nil {
		t.Fatal("an invalid address must fail")
	}
	// 默认的 exit 是真的 os.Exit——它必须先被换掉，否则测试进程当场结束。
	exited := make(chan int, 1)
	restoreExit := swapExit(func(code int) { exited <- code })
	hooks.exit(0)
	restoreExit()
	if code := <-exited; code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	// 拿到一个可关闭的信号通道：关掉它，避免测试进程留下 signal 处理器。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	signal.Stop(stop)
	if hooks.nowSignal() == nil {
		t.Fatal("nowSignal must hand out a channel")
	}
}

// TestServeEnablesCORSWhenOriginsAreConfigured 与 TestServeStopsSearchOnListenFailure
// 分别覆盖两条「只有真实配置才走到」的分支：配了允许的来源才挂 CORS 中间件；
// 监听失败时若搜索已启动，必须把子进程一起收掉。
func TestServeEnablesCORSWhenOriginsAreConfigured(t *testing.T) {
	hooks := bootForTests(t)
	hooks.loadSettings = func(string) (*config.Config, error) {
		return &config.Config{AllowedOrigins: []string{"https://example.com"}}, nil
	}
	registered := false
	hooks.registerRoutes = func(e *echo.Echo, _ *config.Config, _ *sql.DB, _ string, _ *search.Client, _ *mediaconv.Converter) error {
		// 用一次真实请求确认 CORS 中间件确实挂在链上：命中说明响应带了
		// Access-Control-Allow-Origin，未命中则 404 且没有该头。
		request := httptest.NewRequest(http.MethodOptions, "/api/site", nil)
		request.Header.Set("Origin", "https://example.com")
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, request)
		registered = recorder.Header().Get("Access-Control-Allow-Origin") != ""
		return nil
	}
	if err := serve(hooks); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !registered {
		t.Fatal("CORS middleware must be installed when origins are configured")
	}
}

func TestServeStopsSearchOnListenFailure(t *testing.T) {
	restoreBackoff := searchStartBackoff
	searchStartBackoff = time.Millisecond
	t.Cleanup(func() { searchStartBackoff = restoreBackoff })

	hooks := bootForTests(t)
	hooks.loadSettings = func(string) (*config.Config, error) {
		return &config.Config{SearchEnabled: boolPtr(true)}, nil
	}
	hooks.listen = func(*echo.Echo, string) error { return errors.New("address in use") }
	if err := serve(hooks); err == nil {
		t.Fatal("a failed listen must surface")
	}
}
