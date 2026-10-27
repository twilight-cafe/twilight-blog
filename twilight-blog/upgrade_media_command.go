package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"blog-engine/internal/database"
	"blog-engine/internal/logging"
	"blog-engine/internal/mediaconv"
	"blog-engine/internal/model"
	"blog-engine/internal/pidfile"
	"blog-engine/internal/service"
)

// 本文件实现 `<executable> upgrade-media` 子命令。
//
// 它回答的问题是「在这个站点开始强制 WebP 之前传进来的那些图片怎么办」。
// 上传路径现在保证图片一律以 WebP 落盘（见 service.StoreFile），但存量不会因此
// 改变：磁盘上还是当年那个 jpg/png，数据库里记的也是当年的类型。本命令把存量补齐，
// 让「媒体库里全是 WebP」这句话对**整库**成立，而不只是对新上传成立。
//
// 关键设计：**就地更新，不新建记录**。
// 所有引用（articles.cover_media_id、users.avatar_media_id、moment_media.media_id）
// 指向的都是 media.id，而不是文件名或 URL。所以只要 id 不变，改写 storage_name /
// mime_type / size / converted 四列之后，引用自动继续有效，一处都不用重写。
// 反过来说，若选择「新建一条 WebP 记录再改引用」，就要在一个事务里改三张表，
// 而漏改任何一处都会留下指向旧图的引用——这种错误在界面上看不出来，直到那条旧记录
// 被删、外键把引用置空才暴露。
const upgradeMediaCommandUsage = `Usage:
  <executable> upgrade-media [--dry-run] [--all] [--quiet]

Re-encodes the images in the media library that are not stored as WebP yet, so
that pictures uploaded before this behaviour existed get the same treatment as
new ones. Uploads are always stored as WebP; this command is for what is already
on disk.

Each converted item keeps its media id — only its storage name, mime type and
size are updated. Every reference to it therefore stays valid without being
rewritten: articles.cover_media_id, users.avatar_media_id and the moment_media
join table all point at the id, not at a file name.

Options:
      --dry-run   Report what would be converted and change nothing.
      --all       Also re-encode images that are already WebP. Useful after
                  changing the encoder or its settings, which is otherwise
                  skipped as there is nothing to gain.
      --quiet     Print only the closing summary.

Exit status: 0 when every candidate was converted (or there was nothing to do),
1 when at least one image could not be converted, 2 for a usage error.

The site may keep serving while this runs. Each image is converted into a new
file first, then its record is updated, and only then is the old file removed —
so at no point does the database point at a file that does not exist.
`

// upgradeMediaOptions 是 upgrade-media 子命令解析出来的参数。
type upgradeMediaOptions struct {
	dryRun bool
	all    bool
	quiet  bool
}

// upgradeMediaSummary 是一次升级的结果计数。
type upgradeMediaSummary struct {
	// Images 是媒体库里的图片总数（含已经是 WebP 的）。
	Images int
	// AlreadyWebP 是其中本来就存成 WebP 的。
	AlreadyWebP int
	// Candidates 是本次要处理的（Images - AlreadyWebP，--all 时等于 Images）。
	Candidates int
	// Upgraded 与 Failed 是实际处理的结果（--dry-run 时都为 0）。
	Upgraded int
	Failed   int
	DryRun   bool
}

// runUpgradeMediaCommand 执行 upgrade-media 子命令，返回进程退出码。
var configureUpgradeLogging = logging.Configure

func runUpgradeMediaCommand(args []string) int {
	opts, err := parseUpgradeMediaArgs(args)
	if err != nil {
		fmt.Fprintln(stderr(), err)
		return exitUsageOrValidation
	}

	// 与其它子命令一致：先让日志可用，这样一次升级（以及它引出的那些警告）
	// 会在 log.txt 里留下痕迹。
	if err := configureUpgradeLogging(""); err != nil {
		fmt.Fprintf(stderr(), "configure logging: %v\n", err)
		return exitFailure
	}

	summary, err := performUpgradeMedia(opts)
	if err != nil {
		fmt.Fprintf(stderr(), "upgrade-media failed: %v\n", err)
		return exitFailure
	}

	// 收尾摘要与 --quiet 无关：这个开关的说明就是「只打印收尾摘要」，而真正被它
	// 关掉的是循环里的逐条输出。原先这里也一起挡掉了，于是 --quiet 变成一声不吭
	// ——脚本从退出码知道「有东西失败了」，却没有任何线索指出是哪一条。
	reportUpgradeMedia(stdout(), summary)
	// 有失败的条目就以非零退出：脚本要能靠退出码判断「这次升级干净了没有」，
	// 而不是去 grep 输出。
	if summary.Failed > 0 {
		return exitFailure
	}
	return 0
}

// parseUpgradeMediaArgs 解析参数。三个开关都不带值，未知参数一律报错——
// 静默忽略一个拼错的开关会让人以为「按预期跑了」。
func parseUpgradeMediaArgs(args []string) (upgradeMediaOptions, error) {
	var opts upgradeMediaOptions
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.dryRun = true
		case "--all":
			opts.all = true
		case "--quiet":
			opts.quiet = true
		default:
			return opts, fmt.Errorf("unknown argument %q\n\n%s", arg, upgradeMediaCommandUsage)
		}
	}
	return opts, nil
}

// performUpgradeMedia 是命令本体：读配置 → 探测编码器 → 打开库 → 逐张图片重编码。
func performUpgradeMedia(opts upgradeMediaOptions) (upgradeMediaSummary, error) {
	summary := upgradeMediaSummary{DryRun: opts.dryRun}

	// 与 backup / restore 一样，一切路径都相对当前工作目录：这是同一个站点目录，
	// 而「在哪个目录里跑」正是运维最容易搞错的一件事。
	for _, required := range []string{settingsPath, databasePath} {
		if _, err := os.Stat(required); err != nil {
			return summary, fmt.Errorf("%s not found: run this from the site's working directory", required)
		}
	}

	mediaDir := readMediaDir(settingsPath)

	// 编码器在**任何改动之前**就要确认可用：没有它，每一条都会以同样的理由失败，
	// 而那时已经没有任何东西可改动——早退出只是让这件事在第一个字节被读之前发生。
	// --dry-run 也要过这一关：「这台机器上这事能不能干成」正是 dry-run 要回答的问题。
	mediaConv, err := mediaconv.Detect(
		readSiteSetting(settingsPath, "webp_tool", ""),
		readSiteSetting(settingsPath, "webm_tool", ""),
	)
	if err != nil {
		return summary, err
	}

	db, err := database.Open(databasePath)
	if err != nil {
		return summary, err
	}
	defer db.Close()

	repo := model.NewSQLiteMediaRepository(db)
	images, err := repo.ListImagesForWebPUpgrade(context.Background())
	if err != nil {
		return summary, err
	}
	summary.Images = len(images)

	// 站点在跑也照做：每个条目都是「写新文件 → 改库 → 删旧文件」，期间库指向的文件
	// 始终存在，而 SQLite 的 WAL 允许多进程写。只提醒一句——若此刻正好有人在后台
	// 删图，那一条会以「文件不见了」的形式失败并计入 Failed，而不会伤到别的条目。
	if info, running := pidfile.Holder(pidfile.FileName); running {
		fmt.Fprintf(stderr(), "Note: the site appears to be running (pid %d); that is safe, "+
			"but the library is being modified underneath it.\n", info.PID)
	}

	if summary.Images == 0 {
		return summary, nil
	}

	// 库里非 WebP 的图片必然要有文件可读。先整体确认一次，而不是让每一行各报一遍
	// 「打不开」——后者会把「媒体目录根本不在」这个真正的原因淹在几十行噪声里。
	if err := ensureMediaDir(mediaDir); err != nil {
		return summary, err
	}

	mediaService := service.NewMediaService(repo, mediaDir, mediaConv)
	ctx := context.Background()

	for _, media := range images {
		if media.MimeType == webPMimeType && !opts.all {
			summary.AlreadyWebP++
			continue
		}
		summary.Candidates++
		if opts.dryRun {
			continue
		}

		upgraded, err := mediaService.UpgradeToWebP(ctx, &media)
		if err != nil {
			summary.Failed++
			fmt.Fprintf(stderr(), "  id=%d %s: %v\n", media.ID, media.StorageName, err)
			continue
		}
		summary.Upgraded++
		if !opts.quiet {
			fmt.Fprintf(stdout(), "  id=%d %s (%s) -> %s (webp, %d bytes)\n",
				media.ID, media.StorageName, media.MimeType, upgraded.StorageName, upgraded.Size)
		}
	}

	return summary, nil
}

// webPMimeType 是图片转码后的目标类型。service 内部也用同一个字面量拼 plan
// （见 media_service 里的 transcoding），这里独立写一份是因为两边判断的是不同的事：
// 那边描述「要转成什么」，这边只是识别「已经是目标格式了吗」。
const webPMimeType = "image/webp"

// ensureMediaDir 确认媒体目录存在且确实是目录。
func ensureMediaDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("media directory %s cannot be read: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("media directory %s is not a directory", dir)
	}
	return nil
}

// reportUpgradeMedia 打印收尾摘要。
//
// 「已经是 WebP 的有几张」要单独报出来：扫描数为 0 时，运维需要区分「库里没有图片」
// 与「库里的图片早就都是 WebP」——这两件事对应的下一步动作完全不同。
func reportUpgradeMedia(w io.Writer, s upgradeMediaSummary) {
	if s.DryRun {
		fmt.Fprintf(w, "Dry run: %d of %d image(s) would be converted to WebP; %d already are.\n",
			s.Candidates, s.Images, s.AlreadyWebP)
		return
	}
	fmt.Fprintf(w, "Converted %d of %d image(s) to WebP; %d already were.\n",
		s.Upgraded, s.Images, s.AlreadyWebP)
	if s.Failed > 0 {
		fmt.Fprintf(w, "%d image(s) could not be converted; the reasons are on the lines above.\n", s.Failed)
	}
}
