package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 解包时的资源上限。它们防的是**别人的归档**，不是给自己的站点做容量规划——
// 自己的数据有多大，备份那一刻就知道了。
const (
	// DefaultMaxExtractBytes 限制一份归档解压后的总字节数。
	//
	// 一个博客的数据库加媒体通常远小于 8 GiB，而几十 KB 的归档解出几十 GB
	// 正是典型的 gzip 炸弹形态：压缩比可以做到上千倍，磁盘会被瞬间写满。
	DefaultMaxExtractBytes = int64(8) << 30

	// DefaultMaxExtractEntries 限制条目总数。
	//
	// 它防的是另一种形态：零字节但海量条目，撑不爆磁盘却能把 inode 用光。
	DefaultMaxExtractEntries = 200000

	// maxManifestBytes 限制清单本身的大小。清单是我们唯一会整个读进内存的东西，
	// 所以它自己的上限必须先定死。
	maxManifestBytes = 4 << 20
)

// Reader 顺序读取一份归档。
//
// 它持有 gzip 与 tar 的解码状态，所以**只能向前读一次**：Extract 之后
// 这个 Reader 就耗尽了。需要重新读就重新 Open。
type Reader struct {
	tr       *tar.Reader
	manifest Manifest
}

// Open 读归档的第一个条目（清单），返回一个顺序读取器。
//
// 它**不**关闭 r：归档可能来自 os.Stdin，也可能来自内存里的 bytes.Reader，
// 谁创建谁负责。这一点与 io 包的惯例一致。
func Open(r io.Reader) (*Reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		// gzip 头都读不出来，最常见的两种原因是「这不是 gzip 文件」与
		// 「文件被截断」，所以错误里要说清我们本来期待什么。
		return nil, fmt.Errorf("read the archive (is it a .tar.gz produced by this program?): %w", err)
	}

	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the archive is empty")
		}
		return nil, fmt.Errorf("read the archive: %w", err)
	}
	if hdr.Name != ManifestName {
		return nil, fmt.Errorf("the first entry in the archive is %q, want %q: "+
			"this does not look like a twilight-blog backup", hdr.Name, ManifestName)
	}
	if hdr.Size > maxManifestBytes {
		return nil, fmt.Errorf("the archive manifest claims to be %d bytes, which is implausibly large", hdr.Size)
	}

	var manifest Manifest
	if err := json.NewDecoder(io.LimitReader(tr, maxManifestBytes)).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("read the archive manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	return &Reader{tr: tr, manifest: manifest}, nil
}

// Manifest 返回归档的清单。
//
// 返回的是**浅拷贝**：Parts 与内部那个切片共享底层数组。调用方只读它，
// 不要改——真要改就先自己复制一份。
func (r *Reader) Manifest() Manifest {
	manifest := r.manifest
	manifest.Parts = append([]Part(nil), r.manifest.Parts...)
	return manifest
}

// ExtractOptions 调整解包的资源上限。零值表示用默认值。
type ExtractOptions struct {
	MaxBytes   int64
	MaxEntries int
}

// ExtractResult 汇报解包结果。
type ExtractResult struct {
	// Files 是写出的常规文件数。
	Files int
	// Bytes 是它们的总字节数（未压缩）。
	Bytes int64
	// Parts 是确认落盘的清单部分名，顺序与清单一致。
	Parts []string
}

// Extract 把归档的余下条目解包到 root 之下。
//
// 调用顺序上它必须在 Open 之后，且只能调一次。
//
// 安全性基于三条：
//
//   - 每个条目的路径都要过 safeEntryPath（只接受 data/ 之下、且是本地相对路径）；
//   - 只接受常规文件与目录，符号链接/硬链接/设备文件直接拒绝而不是跳过——
//     跳过会让归档的内容与清单对不上，拒绝才能让「这份归档有问题」浮出水面；
//   - 解压总量与条目数封顶，防 gzip 炸弹。
//
// 另外它要求 root **存在且为空**（见 prepareRoot）。这一条不是洁癖：解包路径的
// 安全性有一部分建立在「root 里没有预先埋好的符号链接」之上，允许非空目录就等于
// 把这个前提交给调用方保证，而调用方本来就会用刚创建的临时目录。
func (r *Reader) Extract(root string, opts ExtractOptions) (*ExtractResult, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxExtractBytes
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = DefaultMaxExtractEntries
	}
	if err := prepareRoot(root); err != nil {
		return nil, err
	}

	// 清单里声明了 sha256 的条目，逐一在解包时对账。
	expected := make(map[string]string, len(r.manifest.Parts))
	for _, part := range r.manifest.Parts {
		if part.SHA256 != "" {
			expected[part.Path] = part.SHA256
		}
	}

	result := &ExtractResult{}
	entries := 0

	for {
		hdr, err := r.tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the archive: %w", err)
		}

		entries++
		if entries > opts.MaxEntries {
			return nil, fmt.Errorf("the archive holds more than %d entries: refusing to extract it", opts.MaxEntries)
		}

		rel, err := safeEntryPath(hdr.Name)
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(root, rel)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o700); err != nil {
				return nil, fmt.Errorf("create %s: %w", dest, err)
			}
			continue
		case tar.TypeReg:
			// 落到下面处理。
		default:
			return nil, fmt.Errorf("the archive entry %q has unsupported type %q: "+
				"only regular files and directories are allowed", hdr.Name, hdr.Typeflag)
		}

		if hdr.Size < 0 {
			return nil, fmt.Errorf("the archive entry %q declares a negative size", hdr.Name)
		}
		result.Bytes += hdr.Size
		if result.Bytes > opts.MaxBytes {
			return nil, fmt.Errorf("the archive expands to more than %d bytes: refusing to extract it", opts.MaxBytes)
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
		}

		sum, err := extractFile(dest, r.tr)
		if err != nil {
			return nil, err
		}
		result.Files++

		if want, ok := expected[hdr.Name]; ok && sum != want {
			return nil, fmt.Errorf("%s is corrupt: sha256 is %s but the manifest says %s", hdr.Name, sum, want)
		}
	}

	// 清单里声明过的每一部分都必须真的落盘了。
	//
	// 少了任何一项都说明归档损坏或被裁剪，而「少一个数据库」的还原是一场静默
	// 灾难：站点能起来，只是变成空站，而操作者以为数据已经回来了。
	for _, part := range r.manifest.Parts {
		rel, err := safeEntryPath(part.Path)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(filepath.Join(root, rel)); err != nil {
			return nil, fmt.Errorf("the archive is missing its %s part (%s)", part.Name, part.Path)
		}
		result.Parts = append(result.Parts, part.Name)
	}
	return result, nil
}

// extractFile 写出一个条目并返回它的 sha256。
//
// 两个细节都是必需的：
//
//   - O_EXCL：保证不会跟着一个已存在的路径写下去（尤其是符号链接）。root 必须
//     为空，所以正常情况下这里的文件都不该存在；真撞上说明归档里有重复路径，
//     那本身就值得停下来。这是纵深防御，不是唯一防线。
//   - 权限 0600：归档内容含凭据，别让它们在磁盘上比原来更宽松。
func extractFile(dest string, src io.Reader) (string, error) {
	file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", dest, err)
	}

	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(file, sum), src); err != nil {
		file.Close()
		return "", fmt.Errorf("write %s: %w", dest, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("write %s: %w", dest, err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// prepareRoot 要求 root 存在且为空（不存在则创建）。
func prepareRoot(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(root, 0o700); err != nil {
				return fmt.Errorf("create the extract directory: %w", err)
			}
			return nil
		}
		return fmt.Errorf("inspect the extract directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("the extract target %s is not a directory", root)
	}
	children, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect the extract directory: %w", err)
	}
	if len(children) > 0 {
		return fmt.Errorf("the extract target %s is not empty: refusing to mix archived data with existing files", root)
	}
	return nil
}

// safeEntryPath 校验归档内的路径，返回可供 filepath.Join 使用的本地相对路径。
//
// 它是写入端与解包端**共用**的同一套判据（写入端在 scanEntry 里也会调一次），
// 这样「我们写得出什么」与「我们读得进什么」永远不会漂移。
//
// 拒绝面覆盖了 zip-slip 的各个变体：绝对路径、盘符前缀（Windows 的 C:）、
// 任何位置的 `..` 段、以及 Windows 上的保留设备名（CON、NUL…）——
// filepath.IsLocal 一次性把这几类全部排掉，比手写一串 Contains 可靠得多。
func safeEntryPath(name string) (string, error) {
	if !strings.HasPrefix(name, DataPrefix) {
		return "", fmt.Errorf("the archive entry %q is outside %s", name, DataPrefix)
	}

	rel := strings.TrimPrefix(name, DataPrefix)
	// 目录条目带尾随斜杠（tar 的约定），去掉它才能拼出正确的本地路径。
	rel = strings.TrimSuffix(rel, "/")
	if rel == "" {
		return "", fmt.Errorf("the archive entry %q has an empty path", name)
	}

	// tar 的路径分隔符恒为 '/'，与本机是什么系统无关；先转成平台形式，
	// 否则 Windows 上的 IsLocal 看到的是一整段没有分隔符的名字
	// （于是 `a/../../b` 会被当成一个合法的文件名放过去）。
	local := filepath.FromSlash(rel)
	if !filepath.IsLocal(local) {
		return "", fmt.Errorf("the archive entry %q is not a safe relative path", name)
	}
	return local, nil
}

// RelativePath 把归档内的路径换成解包目录之下的本地相对路径。
//
// 它与 Extract **必须**是同一套换算：解包是按剥掉 data/ 之后的相对路径落盘的
// （root 之下直接就是 blog.db、media/…），所以还原命令要按清单里的路径去暂存
// 目录拿文件，就得用同一个函数。两边各写一份，迟早会出现「归档里有六个部分、
// 还原只找到五个」这种半对半错的状态——而那种状态不会有任何一处报错。
func RelativePath(archivePath string) (string, error) {
	return safeEntryPath(archivePath)
}

// HashFile 算一个文件的 sha256（十六进制小写）。
//
// 备份写完之后顺手再核对一次整卷是很便宜的保险：一份没被校验过的备份，
// 只能算「很可能完整」。
func HashFile(path string) (string, error) {
	return hashFile(path)
}

// IsPart 报告清单里是否含有某个部分。还原端据此决定要不要动对应的目录，
// 免得对一个归档里根本没有的东西做「删除后放置」。
func (m Manifest) IsPart(name string) bool {
	for _, part := range m.Parts {
		if part.Name == name {
			return true
		}
	}
	return false
}

// PartPath 返回某个部分在归档内的路径；不存在时返回空串。
func (m Manifest) PartPath(name string) string {
	for _, part := range m.Parts {
		if part.Name == name {
			return part.Path
		}
	}
	return ""
}
