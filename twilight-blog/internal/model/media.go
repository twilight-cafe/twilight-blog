package model

import "errors"

var ErrMediaNotFound = errors.New("media not found")

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
