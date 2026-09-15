package viewmodel

import (
	"fmt"

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

func NewMediaResponse(media *model.Media) MediaResponse {
	logging.Debugf("[viewmodel] NewMediaResponse called")
	return MediaResponse{
		ID:           media.ID,
		URL:          fmt.Sprintf("/api/media/%d", media.ID),
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
