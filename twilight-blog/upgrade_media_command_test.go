package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blog-engine/internal/database"
	"blog-engine/internal/pidfile"
	"blog-engine/internal/testtool"
)

// upgradeImage 描述一张预先放进媒体库的图片。
type upgradeImage struct {
	// name 是磁盘上的文件名，也就是 media.storage_name。
	name     string
	body     []byte
	mimeType string
	// missing 为真时只写记录、不落文件，用来构造「库里指着、磁盘上没有」。
	missing bool
}

// seedUpgradeSite 在当前工作目录里造一个可升级的站点：真正的 SQLite 库、
// 一批媒体记录、media 目录，以及 settings.json。
//
// webp_tool 指向**测试二进制自己**（由 testtool 冒充编码器）。不这么写的话，
// 「这台机器上探测得到编码器吗」就交给了宿主机环境，而这条命令的成败恰恰完全
// 取决于它——一条会因为开发机没装 cwebp 而变红的用例，最后只会被人删掉。
func seedUpgradeSite(t *testing.T, tool string, images []upgradeImage) {
	t.Helper()

	db, err := database.Open(databasePath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	for _, image := range images {
		converted := 0
		if image.mimeType == webPMimeType {
			converted = 1
		}
		if _, err := db.Exec(
			`INSERT INTO media (storage_name, original_name, media_type, mime_type, size, converted, created_at, deleted_at)
			 VALUES (?, ?, 'image', ?, ?, ?, ?, NULL)`,
			image.name, image.name, image.mimeType, len(image.body), converted, 1,
		); err != nil {
			db.Close()
			t.Fatalf("写入媒体记录失败 (%s): %v", image.name, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}

	files := make(map[string]string)
	for _, image := range images {
		if !image.missing {
			files[image.name] = string(image.body)
		}
	}
	if len(files) > 0 {
		seedTree(t, "media", files)
	} else if err := os.MkdirAll("media", 0o700); err != nil {
		t.Fatalf("创建媒体目录失败: %v", err)
	}

	if err := os.WriteFile(settingsPath,
		[]byte(`{"media_dir":"media","webp_tool":`+quoteJSON(tool)+`}`), 0o600); err != nil {
		t.Fatalf("写 settings.json 失败: %v", err)
	}
}

// quoteJSON 把一个路径写成 JSON 字符串。Windows 路径里的反斜杠必须转义，
// 否则 settings.json 会变成一个解析不了的文件，而症状会是「编码器探测不到」——
// 指向完全错误的方向。
func quoteJSON(value string) string {
	var buf bytes.Buffer
	buf.WriteByte('"')
	for _, r := range value {
		if r == '\\' || r == '"' {
			buf.WriteByte('\\')
		}
		buf.WriteRune(r)
	}
	buf.WriteByte('"')
	return buf.String()
}

// upgradeRow 是断言时要看的几个字段。
type upgradeRow struct {
	id          int64
	storageName string
	mimeType    string
	converted   bool
}

// upgradeMediaRows 按 original_name 读出媒体库的现状。
//
// 断言必须回到库与磁盘上：这条命令的接口面貌只是一个退出码，而它要保证的事情
// （引用仍然有效、磁盘上每个文件都有记录解释）在退出码里一个字都看不到。
func upgradeMediaRows(t *testing.T) map[string]upgradeRow {
	t.Helper()

	db, err := database.Open(databasePath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT id, original_name, storage_name, mime_type, converted FROM media ORDER BY id`)
	if err != nil {
		t.Fatalf("查询媒体记录失败: %v", err)
	}
	defer rows.Close()

	result := make(map[string]upgradeRow)
	for rows.Next() {
		var original string
		var row upgradeRow
		var converted int
		if err := rows.Scan(&row.id, &original, &row.storageName, &row.mimeType, &converted); err != nil {
			t.Fatalf("读取媒体记录失败: %v", err)
		}
		row.converted = converted != 0
		result[original] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历媒体记录失败: %v", err)
	}
	return result
}

// mediaDirFiles 列出媒体目录里的文件名。
func mediaDirFiles(t *testing.T, dir string) []string {
	t.Helper()

	var names []string
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, d.Name())
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		t.Fatalf("遍历 %s 失败: %v", dir, err)
	}
	return names
}

func TestParseUpgradeMediaArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    upgradeMediaOptions
		wantErr bool
	}{
		{name: "no arguments", args: nil, want: upgradeMediaOptions{}},
		{name: "dry run", args: []string{"--dry-run"}, want: upgradeMediaOptions{dryRun: true}},
		{name: "all", args: []string{"--all"}, want: upgradeMediaOptions{all: true}},
		{name: "quiet", args: []string{"--quiet"}, want: upgradeMediaOptions{quiet: true}},
		{
			name: "every flag together",
			args: []string{"--dry-run", "--all", "--quiet"},
			want: upgradeMediaOptions{dryRun: true, all: true, quiet: true},
		},
		// 静默忽略一个拼错的开关会让人以为「按预期跑了」，所以未知参数一律报错。
		{name: "unknown flag", args: []string{"--nope"}, wantErr: true},
		{name: "a stray positional argument", args: []string{"media"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUpgradeMediaArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseUpgradeMediaArgs(%v) succeeded, want an error", tt.args)
				}
				if !strings.Contains(err.Error(), upgradeMediaCommandUsage) {
					t.Error("the error does not carry the usage text; the reader cannot fix the command")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUpgradeMediaArgs(%v) error = %v", tt.args, err)
			}
			if got != tt.want {
				t.Fatalf("parseUpgradeMediaArgs(%v) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

// TestUpgradeMediaConvertsExistingImages 是这条命令的核心用例。
//
// 它钉住三件事：非 WebP 的图片被换成 WebP 且**记录被就地改写**（id 不变）、
// 已经是 WebP 的图片不动、以及磁盘上旧文件被清掉而新文件在。
func TestUpgradeMediaConvertsExistingImages(t *testing.T) {
	useTempInstance(t)
	out, errOut := captureStreams(t, "")
	seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), []upgradeImage{
		{name: "old.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
		{name: "already.webp", body: []byte("pretend this is a WebP"), mimeType: webPMimeType},
	})
	before := upgradeMediaRows(t)

	if code := runUpgradeMediaCommand(nil); code != 0 {
		t.Fatalf("退出码 = %d, 期望 0 (stderr: %s)", code, errOut)
	}

	after := upgradeMediaRows(t)
	original := before["old.png"]
	upgraded := after["old.png"]
	if upgraded.id != original.id {
		t.Errorf("id 从 %d 变成了 %d；所有引用都指向 id，它必须不变", original.id, upgraded.id)
	}
	if upgraded.mimeType != webPMimeType || !upgraded.converted {
		t.Errorf("old.png 的记录 = %+v, 期望 image/webp 且 converted", upgraded)
	}
	if upgraded.storageName == "old.png" {
		t.Error("storage_name 没变；重新编码后的文件需要自己的名字")
	}
	if !strings.HasSuffix(upgraded.storageName, ".webp") {
		t.Errorf("storage_name = %q, 期望 .webp 结尾", upgraded.storageName)
	}

	got, err := os.ReadFile(filepath.Join("media", upgraded.storageName))
	if err != nil {
		t.Fatalf("读取转码产物失败: %v", err)
	}
	if !bytes.Equal(got, testtool.Payload()) {
		t.Error("落盘的不是编码器的产物")
	}
	if _, err := os.Stat(filepath.Join("media", "old.png")); !os.IsNotExist(err) {
		t.Errorf("旧文件还在（stat 错误 = %v），但已经没有记录指向它了", err)
	}

	if untouched := after["already.webp"]; untouched.storageName != "already.webp" || untouched.mimeType != webPMimeType {
		t.Errorf("已经是 WebP 的那张被动了: %+v", untouched)
	}

	if !strings.Contains(out.String(), "Converted 1 of 2 image(s) to WebP; 1 already were.") {
		t.Errorf("摘要不对:\n%s", out)
	}
	if !strings.Contains(out.String(), "-> ") {
		t.Errorf("逐条输出缺失:\n%s", out)
	}
}

// TestUpgradeMediaDryRunChangesNothing 钉住 --dry-run 的语义：报告要说的和真跑
// 一致，而磁盘与数据库一个字节都不许动。
func TestUpgradeMediaDryRunChangesNothing(t *testing.T) {
	useTempInstance(t)
	out, errOut := captureStreams(t, "")
	seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), []upgradeImage{
		{name: "old.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
	})
	before := upgradeMediaRows(t)

	if code := runUpgradeMediaCommand([]string{"--dry-run"}); code != 0 {
		t.Fatalf("退出码 = %d, 期望 0 (stderr: %s)", code, errOut)
	}

	after := upgradeMediaRows(t)
	if after["old.png"] != before["old.png"] {
		t.Errorf("dry-run 改了记录: %+v -> %+v", before["old.png"], after["old.png"])
	}
	if names := mediaDirFiles(t, "media"); len(names) != 1 || names[0] != "old.png" {
		t.Errorf("dry-run 动了磁盘: %v", names)
	}
	if !strings.Contains(out.String(), "Dry run: 1 of 1 image(s) would be converted to WebP; 0 already are.") {
		t.Errorf("dry-run 摘要不对:\n%s", out)
	}
}

// TestUpgradeMediaAllReencodesAlreadyWebP 覆盖 --all：
// 换了编码器或改了它的参数之后，连已经合格的图片也要重来一遍。
func TestUpgradeMediaAllReencodesAlreadyWebP(t *testing.T) {
	useTempInstance(t)
	out, errOut := captureStreams(t, "")
	seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), []upgradeImage{
		{name: "old.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
		{name: "already.webp", body: []byte("pretend this is a WebP"), mimeType: webPMimeType},
	})

	if code := runUpgradeMediaCommand([]string{"--all"}); code != 0 {
		t.Fatalf("退出码 = %d, 期望 0 (stderr: %s)", code, errOut)
	}

	after := upgradeMediaRows(t)
	for _, name := range []string{"old.png", "already.webp"} {
		if after[name].storageName == name {
			t.Errorf("%s 没有被重新编码", name)
		}
	}
	if !strings.Contains(out.String(), "Converted 2 of 2 image(s) to WebP; 0 already were.") {
		t.Errorf("--all 的摘要不对:\n%s", out)
	}
}

// TestUpgradeMediaQuietPrintsOnlyTheSummary 覆盖 --quiet：脚本要的是退出码，
// 人的终端里只该留下收尾那一行。
func TestUpgradeMediaQuietPrintsOnlyTheSummary(t *testing.T) {
	useTempInstance(t)
	out, errOut := captureStreams(t, "")
	seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), []upgradeImage{
		{name: "old.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
	})

	if code := runUpgradeMediaCommand([]string{"--quiet"}); code != 0 {
		t.Fatalf("退出码 = %d, 期望 0 (stderr: %s)", code, errOut)
	}

	if strings.Contains(out.String(), "-> ") {
		t.Errorf("--quiet 仍然打印了逐条输出:\n%s", out)
	}
	if !strings.Contains(out.String(), "Converted 1 of 1 image(s) to WebP; 0 already were.") {
		t.Errorf("--quiet 把摘要也吞了:\n%s", out)
	}
}

// TestUpgradeMediaFailsWhenAnImageCannotBeConverted 钉住退出码的语义：
// 有失败条目就以 1 退出，脚本不必去 grep 输出。
func TestUpgradeMediaFailsWhenAnImageCannotBeConverted(t *testing.T) {
	useTempInstance(t)
	out, errOut := captureStreams(t, "")
	seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), []upgradeImage{
		{name: "there.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
		{name: "gone.png", mimeType: "image/png", missing: true},
	})

	if code := runUpgradeMediaCommand(nil); code != exitFailure {
		t.Fatalf("退出码 = %d, 期望 %d", code, exitFailure)
	}

	if !strings.Contains(errOut.String(), "gone.png") {
		t.Errorf("失败条目没有报到 stderr 上:\n%s", errOut)
	}
	if !strings.Contains(out.String(), "1 image(s) could not be converted") {
		t.Errorf("摘要没有提到失败数:\n%s", out)
	}
	// 失败的那条不许留下半成品，成功的那条照常完成。
	if _, err := os.Stat(filepath.Join("media", "there.png")); !os.IsNotExist(err) {
		t.Error("成功的条目没有完成升级")
	}
	rows := upgradeMediaRows(t)
	if rows["gone.png"].storageName != "gone.png" {
		t.Errorf("失败条目的记录被改了: %+v", rows["gone.png"])
	}
}

// TestUpgradeMediaReportsAnEmptyLibrary 覆盖扫描数为 0。
//
// 「库里没有图片」与「库里的图片早就都是 WebP」对应的下一步动作完全不同，
// 所以这两句话不能合并成一句。
func TestUpgradeMediaReportsAnEmptyLibrary(t *testing.T) {
	useTempInstance(t)
	out, errOut := captureStreams(t, "")
	seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), nil)

	if code := runUpgradeMediaCommand(nil); code != 0 {
		t.Fatalf("退出码 = %d, 期望 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out.String(), "Converted 0 of 0 image(s) to WebP; 0 already were.") {
		t.Errorf("空库的摘要不对:\n%s", out)
	}
}

// TestUpgradeMediaRejectsUnknownArguments 覆盖用法错误的退出码。
func TestUpgradeMediaRejectsUnknownArguments(t *testing.T) {
	useTempInstance(t)
	_, errOut := captureStreams(t, "")

	if code := runUpgradeMediaCommand([]string{"--verbose"}); code != exitUsageOrValidation {
		t.Fatalf("退出码 = %d, 期望 %d", code, exitUsageOrValidation)
	}
	if !strings.Contains(errOut.String(), "unknown argument") {
		t.Errorf("stderr 没有说明原因:\n%s", errOut)
	}
}

// TestUpgradeMediaRequiresTheSiteFiles 覆盖「必须在站点目录里跑」这条前置检查。
//
// 这是在替运维挡一次容易犯的错：路径全是相对工作目录的（与主进程一致），
// 在错误的目录里跑会安静地什么都不做——或者更糟，在另一个站点上做事。
func TestUpgradeMediaRequiresTheSiteFiles(t *testing.T) {
	t.Run("no settings.json", func(t *testing.T) {
		useTempInstance(t)
		_, errOut := captureStreams(t, "")

		if code := runUpgradeMediaCommand(nil); code != exitFailure {
			t.Fatalf("退出码 = %d, 期望 %d", code, exitFailure)
		}
		if !strings.Contains(errOut.String(), settingsPath) {
			t.Errorf("stderr 没有说缺哪个文件:\n%s", errOut)
		}
	})

	t.Run("no blog.db", func(t *testing.T) {
		useTempInstance(t)
		_, errOut := captureStreams(t, "")
		if err := os.WriteFile(settingsPath, []byte(`{"media_dir":"media"}`), 0o600); err != nil {
			t.Fatalf("写 settings.json 失败: %v", err)
		}

		if code := runUpgradeMediaCommand(nil); code != exitFailure {
			t.Fatalf("退出码 = %d, 期望 %d", code, exitFailure)
		}
		if !strings.Contains(errOut.String(), databasePath) {
			t.Errorf("stderr 没有说缺哪个文件:\n%s", errOut)
		}
	})

	t.Run("blog.db is not a database", func(t *testing.T) {
		useTempInstance(t)
		_, errOut := captureStreams(t, "")
		if err := os.WriteFile(databasePath, []byte("this is not SQLite, it is prose"), 0o600); err != nil {
			t.Fatalf("写 blog.db 失败: %v", err)
		}
		if err := os.WriteFile(settingsPath, []byte(`{"media_dir":"media"}`), 0o600); err != nil {
			t.Fatalf("写 settings.json 失败: %v", err)
		}

		if code := runUpgradeMediaCommand(nil); code != exitFailure {
			t.Fatalf("退出码 = %d, 期望 %d", code, exitFailure)
		}
		if errOut.Len() == 0 {
			t.Error("打开坏库失败时没有任何输出")
		}
	})
}

// TestUpgradeMediaFailsWithoutAnEncoder 钉住「编码器在改动任何东西之前就要确认」。
//
// PATH 被清空、settings.json 也没指路径，于是探测必然失败。此时命令必须在第一个
// 字节被读之前退出——否则每条图片都会以同一个理由失败，而那时它们已经被动过了。
func TestUpgradeMediaFailsWithoutAnEncoder(t *testing.T) {
	useTempInstance(t)
	_, errOut := captureStreams(t, "")
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	seedUpgradeSite(t, "", []upgradeImage{
		{name: "old.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
	})

	if code := runUpgradeMediaCommand(nil); code != exitFailure {
		t.Fatalf("退出码 = %d, 期望 %d (stderr: %s)", code, exitFailure, errOut)
	}
	if !strings.Contains(errOut.String(), "no WebP encoder is available") {
		t.Errorf("stderr 没有说清是编码器的问题:\n%s", errOut)
	}
	if names := mediaDirFiles(t, "media"); len(names) != 1 || names[0] != "old.png" {
		t.Errorf("拒绝之后磁盘被动了: %v", names)
	}
	// --dry-run 也要过这一关：「这台机器上这事能不能干成」正是它要回答的问题。
	if code := runUpgradeMediaCommand([]string{"--dry-run"}); code != exitFailure {
		t.Errorf("--dry-run 的退出码 = %d, 期望 %d", code, exitFailure)
	}
}

// TestUpgradeMediaReportsABrokenRow 覆盖「库读得开、但有一行读不出来」。
//
// 构造是把某一行的 size 写成一段非数字文本。SQLite 是动态类型的，这个值会以 TEXT
// 存下来，于是扫描到 int64 时会失败——这正是「有人手工改过库、或者上一版写过别的
// 形状」时的症状。命令必须如实失败，而不是把它当成没有图片。
func TestUpgradeMediaReportsABrokenRow(t *testing.T) {
	useTempInstance(t)
	_, errOut := captureStreams(t, "")
	seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), []upgradeImage{
		{name: "old.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
	})

	db, err := database.Open(databasePath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if _, err := db.Exec(`UPDATE media SET size = 'not-a-number'`); err != nil {
		db.Close()
		t.Fatalf("写入坏数据失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}

	if code := runUpgradeMediaCommand(nil); code != exitFailure {
		t.Fatalf("退出码 = %d, 期望 %d", code, exitFailure)
	}
	if errOut.Len() == 0 {
		t.Error("读不出某一行时没有任何输出")
	}
	if names := mediaDirFiles(t, "media"); len(names) != 1 || names[0] != "old.png" {
		t.Errorf("读库就失败时不该动磁盘: %v", names)
	}
}

// TestUpgradeMediaNotesARunningSite 覆盖「站点正在跑」的提示。
//
// 在线升级是**允许**的（每个条目都是写新文件 → 改库 → 删旧文件，期间库指向的文件
// 始终存在），所以这是一条提示而不是拒止——但它必须出现，否则运维不会知道
// 媒体库正在被自己脚下改。
func TestUpgradeMediaNotesARunningSite(t *testing.T) {
	useTempInstance(t)
	out, errOut := captureStreams(t, "")
	seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), []upgradeImage{
		{name: "old.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
	})

	handle, err := pidfile.Acquire(pidfile.FileName)
	if err != nil {
		t.Fatalf("写入 pid 文件失败: %v", err)
	}
	defer handle.Release()

	if code := runUpgradeMediaCommand(nil); code != 0 {
		t.Fatalf("退出码 = %d, 期望 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut.String(), "the site appears to be running") {
		t.Errorf("没有提示站点在跑:\n%s", errOut)
	}
	if !strings.Contains(out.String(), "Converted 1 of 1") {
		t.Errorf("提示归提示，升级本身要照做:\n%s", out)
	}
}

// TestUpgradeMediaRequiresAUsableMediaDirectory 覆盖媒体目录这一道整体前置检查。
//
// 先整体确认一次，而不是让每一行各报一遍「打不开」——后者会把「媒体目录根本不在」
// 这个真正的原因淹在几十行噪声里。
func TestUpgradeMediaRequiresAUsableMediaDirectory(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		useTempInstance(t)
		_, errOut := captureStreams(t, "")
		seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), []upgradeImage{
			{name: "old.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
		})
		if err := os.RemoveAll("media"); err != nil {
			t.Fatalf("删除媒体目录失败: %v", err)
		}

		if code := runUpgradeMediaCommand(nil); code != exitFailure {
			t.Fatalf("退出码 = %d, 期望 %d", code, exitFailure)
		}
		if !strings.Contains(errOut.String(), "cannot be read") {
			t.Errorf("stderr 没说清原因:\n%s", errOut)
		}
	})

	t.Run("not a directory", func(t *testing.T) {
		useTempInstance(t)
		_, errOut := captureStreams(t, "")
		seedUpgradeSite(t, testtool.Fake(t, testtool.ModeOK), []upgradeImage{
			{name: "old.png", body: []byte("pretend this is a PNG"), mimeType: "image/png"},
		})
		if err := os.RemoveAll("media"); err != nil {
			t.Fatalf("删除媒体目录失败: %v", err)
		}
		if err := os.WriteFile("media", []byte("a file where a directory should be"), 0o600); err != nil {
			t.Fatalf("创建同名文件失败: %v", err)
		}

		if code := runUpgradeMediaCommand(nil); code != exitFailure {
			t.Fatalf("退出码 = %d, 期望 %d", code, exitFailure)
		}
		if !strings.Contains(errOut.String(), "is not a directory") {
			t.Errorf("stderr 没说清原因:\n%s", errOut)
		}
	})
}

// TestEnsureMediaDir 直接钉住那个小检查的三条出路。
func TestEnsureMediaDir(t *testing.T) {
	dir := t.TempDir()
	if err := ensureMediaDir(dir); err != nil {
		t.Errorf("ensureMediaDir(目录) error = %v", err)
	}

	missing := filepath.Join(dir, "nope")
	if err := ensureMediaDir(missing); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("ensureMediaDir(不存在的路径) error = %v", err)
	}

	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	if err := ensureMediaDir(file); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("ensureMediaDir(文件) error = %v", err)
	}
}

// TestReportUpgradeMedia 直接覆盖摘要的三种形状（真跑 / dry-run / 有失败）。
func TestReportUpgradeMedia(t *testing.T) {
	tests := []struct {
		name     string
		summary  upgradeMediaSummary
		contains []string
		absent   []string
	}{
		{
			name:     "a clean run",
			summary:  upgradeMediaSummary{Images: 3, AlreadyWebP: 1, Candidates: 2, Upgraded: 2},
			contains: []string{"Converted 2 of 3 image(s) to WebP; 1 already were."},
			absent:   []string{"Dry run", "could not be converted"},
		},
		{
			name:     "a dry run",
			summary:  upgradeMediaSummary{Images: 3, AlreadyWebP: 1, Candidates: 2, DryRun: true},
			contains: []string{"Dry run: 2 of 3 image(s) would be converted to WebP; 1 already are."},
			absent:   []string{"could not be converted"},
		},
		{
			name:     "a run with failures",
			summary:  upgradeMediaSummary{Images: 3, AlreadyWebP: 1, Candidates: 2, Upgraded: 1, Failed: 1},
			contains: []string{"Converted 1 of 3 image(s) to WebP; 1 already were.", "1 image(s) could not be converted"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			reportUpgradeMedia(&buf, tt.summary)

			for _, want := range tt.contains {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("报告里少了 %q:\n%s", want, buf.String())
				}
			}
			for _, unwanted := range tt.absent {
				if strings.Contains(buf.String(), unwanted) {
					t.Errorf("报告里不该有 %q:\n%s", unwanted, buf.String())
				}
			}
		})
	}
}
