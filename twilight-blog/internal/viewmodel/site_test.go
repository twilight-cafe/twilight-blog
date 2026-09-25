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

// TestSiteSettingsRequestValidateHomePageSize 钉住首页每页文章数的校验：
// 与行数同构的三态字段，nil = 保持现值，显式值必须落在 [Min, Max] 内。
func TestSiteSettingsRequestValidateHomePageSize(t *testing.T) {
	// 字段省略：只改描述的旧客户端必须照常工作。
	omit := SiteSettingsRequest{Description: "d"}
	if err := omit.Validate(); err != nil {
		t.Fatalf("Validate() with an omitted page size error = %v", err)
	}

	// 边界值与中间值都收。
	for _, good := range []int{MinHomePageSize, 6, MaxHomePageSize} {
		value := good
		req := SiteSettingsRequest{HomePageSize: &value}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() with home_page_size=%d error = %v", good, err)
		}
	}

	// 区间外拒绝：0 与负数没有「夹到 1」的说法；上界之外尤其危险——
	// 首页发出的 page_size 会被列表端点按 handler.maxPageSize 校验，
	// 放行 13 就等于让首页第一次请求必然 400。
	for _, bad := range []int{-1, 0, MaxHomePageSize + 1, 100} {
		value := bad
		req := SiteSettingsRequest{HomePageSize: &value}
		if err := req.Validate(); err == nil {
			t.Fatalf("Validate() accepted home_page_size=%d", bad)
		}
	}

	// 两个数字字段互不干扰：行数合法而页数非法，必须仍然被拒。
	lines := 20
	badSize := 0
	req := SiteSettingsRequest{CodeBlockMaxLines: &lines, HomePageSize: &badSize}
	if err := req.Validate(); err == nil {
		t.Fatal("Validate() accepted a legal line count alongside an illegal page size")
	}
}

// TestSiteSettingsResponseAlwaysEmitsHomePageSize 钉住序列化形状：
// home_page_size 必须**恒输出**（不带 omitempty）。前端设置页靠它回显当前值，
// 字段缺席会让「服务端没下发」与「值就是 0」无法区分。
func TestSiteSettingsResponseAlwaysEmitsHomePageSize(t *testing.T) {
	data, err := json.Marshal(SiteSettingsResponse{Description: "", AvatarURL: "", HomePageSize: 0})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"home_page_size":0`) {
		t.Fatalf("response JSON %s does not always emit home_page_size", data)
	}

	data, err = json.Marshal(SiteSettingsResponse{Description: "d", AvatarURL: "/api/media/1", HomePageSize: 8})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"home_page_size":8`) {
		t.Fatalf("response JSON %s lost the configured page size", data)
	}
}

// TestSiteSettingsRequestOmitsNilHomePageSize 钉住请求侧的 omitempty：
// nil 时 JSON 里不能出现 home_page_size 键，服务端据此判定「本次不改动」。
func TestSiteSettingsRequestOmitsNilHomePageSize(t *testing.T) {
	data, err := json.Marshal(SiteSettingsRequest{Description: "d"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(data), "home_page_size") {
		t.Fatalf("request JSON %s emitted a nil page size", data)
	}
}

// TestDefaultHomePageSizeIsPositive 钉住视图层的兜底常量是个正数。
//
// 它只在「配置对象被构造得不完整」时兜底，但一旦是 0，外壳就会下发
// data-home-page-size="0"，首页随即发出一个 page_size=0 的列表请求。
func TestDefaultHomePageSizeIsPositive(t *testing.T) {
	if DefaultHomePageSize < 1 {
		t.Fatalf("DefaultHomePageSize = %d, want a positive fallback", DefaultHomePageSize)
	}
}

// TestSiteSettingsRequestValidateBorderRadius 钉住圆角的校验，重点在 0。
//
// 与上面两个数字字段同构（三态、边界值都收、区间外拒绝），唯一的区别是
// 下界 0 必须**被接受**：0 表示直角，是正当的风格选择。若这里写成
// `radius <= 0` 早退（从 home_page_size「0 即未设置」抄过来的话就会这样），
// 站长提交 0 会拿到 400，而界面上「0 为直角」的说明还在，等于功能失效。
func TestSiteSettingsRequestValidateBorderRadius(t *testing.T) {
	// 字段省略：只改描述的旧客户端必须照常工作。
	omit := SiteSettingsRequest{Description: "d"}
	if err := omit.Validate(); err != nil {
		t.Fatalf("Validate() with an omitted radius error = %v", err)
	}

	// 0 是本次改动最要紧的一条：必须通过。
	zero := 0
	if err := (&SiteSettingsRequest{BorderRadius: &zero}).Validate(); err != nil {
		t.Fatalf("Validate() rejected border_radius=0, but 0 (square) is a legitimate value: %v", err)
	}

	// 边界值与中间值都收。
	for _, good := range []int{MinBorderRadius, 1, 8, MaxBorderRadius} {
		value := good
		req := SiteSettingsRequest{BorderRadius: &value}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() with border_radius=%d error = %v", good, err)
		}
	}

	// 区间外拒绝（而不是夹取）。
	for _, bad := range []int{-1, MaxBorderRadius + 1, 100} {
		value := bad
		req := SiteSettingsRequest{BorderRadius: &value}
		if err := req.Validate(); err == nil {
			t.Fatalf("Validate() accepted border_radius=%d", bad)
		}
	}

	// 字段之间互不干扰：圆角合法而页数非法，必须仍然被拒。
	radius := 8
	badSize := 0
	req := SiteSettingsRequest{BorderRadius: &radius, HomePageSize: &badSize}
	if err := req.Validate(); err == nil {
		t.Fatal("Validate() accepted a legal radius alongside an illegal page size")
	}
}

// TestSiteSettingsResponseAlwaysEmitsBorderRadius 钉住序列化形状：
// border_radius 必须**恒输出**（不带 omitempty）。
//
// 而且它比另外两项多一层风险：**0 是常见值**。若字段带上 omitempty，
// 站长为「直角」保存后，GET /api/site 的响应里会缺 border_radius 这个键，
// 前端回退到默认值 4 —— 于是「设置页显示 0、页面却是圆的」，且完全静默。
func TestSiteSettingsResponseAlwaysEmitsBorderRadius(t *testing.T) {
	data, err := json.Marshal(SiteSettingsResponse{Description: "", AvatarURL: "", BorderRadius: 0})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"border_radius":0`) {
		t.Fatalf("response JSON %s does not emit border_radius when it is 0", data)
	}

	data, err = json.Marshal(SiteSettingsResponse{Description: "d", AvatarURL: "/api/media/1", BorderRadius: 16})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"border_radius":16`) {
		t.Fatalf("response JSON %s lost the configured radius", data)
	}
}

// TestSiteSettingsRequestOmitsNilBorderRadius 钉住请求侧的 omitempty：
// nil 时 JSON 里不能出现 border_radius 键，服务端据此判定「本次不改动」。
//
// 与 0 的对照是本条的意义所在：`omitempty` 作用在**指针**上，
// 因此 `&0` 会被序列化成 `"border_radius":0` 而不是消失。这两个用例一起
// 才说明「不改动」与「改成 0」在协议层面是可区分的。
func TestSiteSettingsRequestOmitsNilBorderRadius(t *testing.T) {
	data, err := json.Marshal(SiteSettingsRequest{Description: "d"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(data), "border_radius") {
		t.Fatalf("request JSON %s emitted a nil radius", data)
	}

	// 对照：显式 0 必须出现，否则「保存直角」会退化成「不改动」。
	zero := 0
	data, err = json.Marshal(SiteSettingsRequest{BorderRadius: &zero})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"border_radius":0`) {
		t.Fatalf("request JSON %s swallowed an explicit 0, making \"square\" indistinguishable from \"leave unchanged\"", data)
	}
}

// TestDefaultBorderRadiusIsNonNegative 钉住视图层的兜底常量是合法的。
//
// 兜底只在「配置对象被构造得不完整」时生效，但一旦是个越界值（比如 -1），
// 外壳就会下发 data-border-radius="-1"，前端的区间校验会把它当非法值再兜回默认，
// 于是「兜底」与「非法」互相抵消，看上去一切正常、实际配置被静默忽略。
func TestDefaultBorderRadiusIsNonNegative(t *testing.T) {
	if DefaultBorderRadius < MinBorderRadius || DefaultBorderRadius > MaxBorderRadius {
		t.Fatalf("DefaultBorderRadius = %d, want a value inside [%d, %d]", DefaultBorderRadius, MinBorderRadius, MaxBorderRadius)
	}
}
