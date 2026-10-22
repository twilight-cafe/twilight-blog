package model

import "errors"

var ErrMediaNotFound = errors.New("media not found")

// ErrInvalidMediaName 表示一个新的展示名不合法（空、超长、含控制字符或路径分隔符）。
//
// 它与「名字里带路径分隔符 = 有穿越风险」无关——展示名从不参与磁盘路径拼接
// （磁盘名是独立的 storage_name，见 Media.StorageName）。这条校验防的是另一类
// 麻烦：控制字符会把日志与界面撑断，而带分隔符的名字会让人误以为它能当路径用。
// 放在 model 层是因为服务层要返回它、handler 要按它映射 HTTP 状态码，
// 与 ErrTagNameExists 同一考虑。
var ErrInvalidMediaName = errors.New("invalid media name")

// MaxMediaOriginalNameLength 是展示名（original_name）的长度上限，按**字符数**
// 而不是字节数——否则一个中文名会因为 UTF-8 占 3 字节而被误判超长，
// 与 MaxSiteDescriptionLength 同一口径。
//
// 它只是展示名的上限，不是文件系统限制：这个名字既不会落盘也不会进响应头，
// 200 个字符足够表达「这是哪张图」，同时挡住把整个段落粘进来的误操作。
const MaxMediaOriginalNameLength = 200

// MediaTypeImage 是「图片」这一逻辑分类的取值。
//
// 分类只有 image / audio / video 三种（见 service.classify 的判定），
// 而「头图只能是图片」这条规则会同时被服务层与（测试）反复引用，
// 因此这个字面量需要一个共同来源——两处各写一份 "image"，
// 改错一处不会有任何编译错误，只会让某条校验悄悄失效。
const MediaTypeImage = "image"

// Media 是一条媒体记录（图片 / 音视频）。
//
// StorageName 是磁盘上的实际文件名（由服务层生成、唯一），OriginalName 是用户
// 上传时的原始名——两者都只是「名字」，路径拼接由服务层负责，不要在这里拼。
// MediaType 是逻辑分类，MimeType 是探测出的真实类型。
// Converted 表示该文件是否经过 mediaconv 转码（如视频压制成 web 友好格式）。
//
// 注意：本结构体虽有 DeletedAt，但当前仓库所有写入路径都不会设置它，
// media 只有硬删（见 SQLiteMediaRepository.Delete）。
type Media struct {
	ID           int64
	StorageName  string
	OriginalName string
	MediaType    string
	MimeType     string
	Size         int64
	Converted    bool
	CreatedAt    int64
	DeletedAt    *int64
}
