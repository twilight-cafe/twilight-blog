package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSiteSettingsRequestValidate 钉住 HTTP 边界的第一道校验：
// 描述走既有的清洗与长度规则；行数是三态——nil（字段省略）=保持现值，
// 显式给出的值必须在区间内，负数或 0 一律拒绝而不是夹取到边界。
func TestSiteSettingsRequestValidate(t *testing.T) {
	// 字段省略：只改描述的旧客户端必须照常工作。
	omit := SiteSettingsRequest{Description: " 新描述 "}
	if err := omit.Validate(); err != nil {
		t.Fatalf("Validate() with an omitted line count error = %v", err)
	}
	if omit.Description != "新描述" {
		t.Fatalf("description = %q, want the collapsed text", omit.Description)
	}

	// 边界值与中间值都收。
	for _, good := range []int{MinCodeBlockLines, 20, MaxCodeBlockLines} {
		req := SiteSettingsRequest{CodeBlockMaxLines: &good}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() with lines=%d error = %v", good, err)
		}
	}

	// 区间外拒绝：0 与负数没有「夹到 3」的说法，管理界面已提示区间，
	// 静默夹取会让保存结果与输入框显示不一致。
	for _, bad := range []int{-5, 0, 1, 2, MaxCodeBlockLines + 1} {
		value := bad
		req := SiteSettingsRequest{CodeBlockMaxLines: &value}
		if err := req.Validate(); err == nil {
			t.Fatalf("Validate() accepted code_block_max_lines=%d", bad)
		}
	}

	// 描述长度上限不变（沿用既有规则，行数不干扰它）。
	long := strings.Repeat("长", MaxSiteDescriptionLength+1)
	req := SiteSettingsRequest{Description: long}
	if err := req.Validate(); err == nil {
		t.Fatal("Validate() accepted an over-length description")
	}
}

// TestSiteSettingsResponseAlwaysEmitsCodeLines 钉住序列化形状：
// code_block_max_lines 必须**恒输出**（不带 omitempty）。它是渲染参数而不是
// 状态位，字段缺席会让前端分不清「服务端没下发」与「该用默认值」。
// 0 是有意义的取值场景（仍须输出键），这条断言同时守住 omitempty 回潮。
func TestSiteSettingsResponseAlwaysEmitsCodeLines(t *testing.T) {
	data, err := json.Marshal(SiteSettingsResponse{Description: "", AvatarURL: "", CodeBlockMaxLines: 0})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"code_block_max_lines":0`) {
		t.Fatalf("response JSON %s does not always emit code_block_max_lines", data)
	}

	data, err = json.Marshal(SiteSettingsResponse{Description: "d", AvatarURL: "/api/media/1", CodeBlockMaxLines: 42})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"code_block_max_lines":42`) {
		t.Fatalf("response JSON %s lost the configured line count", data)
	}
}

// TestSiteSettingsRequestOmitsNilLines 钉住请求侧的 omitempty：
// nil 时 JSON 里不能出现 code_block_max_lines 键——服务端把「键缺席」解释为
// 保持现值，键带着 null 反而要看服务端是否容忍。
func TestSiteSettingsRequestOmitsNilLines(t *testing.T) {
	data, err := json.Marshal(SiteSettingsRequest{Description: "d"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(data), "code_block_max_lines") {
		t.Fatalf("request JSON %s emitted a nil line count", data)
	}
}
