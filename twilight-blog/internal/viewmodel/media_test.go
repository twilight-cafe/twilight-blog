package viewmodel

import (
	"strings"
	"testing"

	"blog-engine/internal/model"
)

// TestRenameMediaRequestValidate 钉住 HTTP 边界上能判的那几条：
// 去首尾空白、非空、字符数上限。
//
// 这里**刻意不判**扩展名与路径分隔符：扩展名要跟着库里那条记录的现有扩展名走
// （只有服务层查得到），而非法字符与服务层共用同一份权威规则
// （见 service.normalizeMediaName）。两处各写一份迟早漂移，症状是「界面说合法、
// 服务端拒绝」或者反过来——两边都只在真机上才被发现。
//
// 长度按**字符数**而不是字节数：一个中文名占 3 字节，按字节判会让中文用户
// 莫名其妙地超限（与站点描述同一口径）。
func TestRenameMediaRequestValidate(t *testing.T) {
	t.Run("去空白后可用", func(t *testing.T) {
		req := RenameMediaRequest{OriginalName: "  猫咪.png  "}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if req.OriginalName != "猫咪.png" {
			t.Fatalf("OriginalName = %q, want the trimmed value", req.OriginalName)
		}
	})

	t.Run("空与纯空白被拒", func(t *testing.T) {
		for _, raw := range []string{"", "   ", "\t"} {
			if err := (&RenameMediaRequest{OriginalName: raw}).Validate(); err == nil {
				t.Fatalf("Validate() accepted %q", raw)
			}
		}
	})

	t.Run("上限按字符数", func(t *testing.T) {
		// 200 个中文字：按字节算是 600，按字符算正好在上限上。
		atLimit := &RenameMediaRequest{OriginalName: strings.Repeat("字", model.MaxMediaOriginalNameLength)}
		if err := atLimit.Validate(); err != nil {
			t.Fatalf("Validate() rejected a name of exactly %d characters: %v", model.MaxMediaOriginalNameLength, err)
		}

		over := &RenameMediaRequest{OriginalName: strings.Repeat("字", model.MaxMediaOriginalNameLength+1)}
		if err := over.Validate(); err == nil {
			t.Fatalf("Validate() accepted %d characters", model.MaxMediaOriginalNameLength+1)
		}
	})
}
