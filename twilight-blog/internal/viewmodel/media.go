package viewmodel

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"blog-engine/internal/model"

	"blog-engine/internal/logging"
)

// MediaResponse 是媒体的对外形状。
// URL 由服务端按 /api/media/{id} 拼出，而不是回传磁盘上的文件名——
// StorageName 属于实现细节，不该泄漏给客户端。
type MediaResponse struct {
	ID           int64  `json:"id"`
	URL          string `json:"url"`
	OriginalName string `json:"original_name"`
	MediaType    string `json:"media_type"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	Converted    bool   `json:"converted"`
	CreatedAt    int64  `json:"created_at"`
	DeletedAt    *int64 `json:"deleted_at,omitempty"`
}

// MediaURL 拼出一张媒体的站内地址。
//
// 抽成函数是为了让「媒体地址长什么样」只有一个定义点：站点信息（站长头像）与
// 媒体列表都用它。前端 SDK 里有一个同名的 mediaUrl（twilight-blog-api），
// 两处必须给出同一种形状，否则头像在服务端渲染与客户端渲染下会是两个地址。
func MediaURL(id int64) string {
	return fmt.Sprintf("/api/media/%d", id)
}

func NewMediaResponse(media *model.Media) MediaResponse {
	logging.Debugf("[viewmodel] NewMediaResponse called")
	return MediaResponse{
		ID:           media.ID,
		URL:          MediaURL(media.ID),
		OriginalName: media.OriginalName,
		MediaType:    media.MediaType,
		MimeType:     media.MimeType,
		Size:         media.Size,
		Converted:    media.Converted,
		CreatedAt:    media.CreatedAt,
		DeletedAt:    media.DeletedAt,
	}
}

func NewMediaResponseList(media []model.Media) []MediaResponse {
	logging.Debugf("[viewmodel] NewMediaResponseList called")
	result := make([]MediaResponse, 0, len(media))
	for i := range media {
		result = append(result, NewMediaResponse(&media[i]))
	}
	return result
}

// RenameMediaRequest 是媒体改名请求（PATCH /api/admin/media/:id）。
//
// 只有展示名一个字段：磁盘文件名（storage_name）由服务层生成且与引用无关，
// 不允许客户端指定——放开它等于让调用方决定服务端往哪个文件写/读。
type RenameMediaRequest struct {
	OriginalName string `json:"original_name"`
}

// Validate 做 HTTP 边界上能做的检查：去空白、非空、长度上限。
//
// 「扩展名沿用现有展示名」与非法字符的判定**不在这里**：前者需要这条记录当前的
// 名字（只有服务层查得到），后者与服务层共用同一份权威规则
// （见 service.normalizeMediaName）。两处各写一份迟早会漂移，而漂移的表现是
// 「界面说合法、服务端拒绝」或反过来。
func (r *RenameMediaRequest) Validate() error {
	r.OriginalName = strings.TrimSpace(r.OriginalName)
	if r.OriginalName == "" {
		return fmt.Errorf("original_name is required")
	}
	if utf8.RuneCountInString(r.OriginalName) > model.MaxMediaOriginalNameLength {
		return fmt.Errorf("original_name must be at most %d characters", model.MaxMediaOriginalNameLength)
	}
	return nil
}
