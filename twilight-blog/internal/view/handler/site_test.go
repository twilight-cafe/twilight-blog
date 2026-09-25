package handler

import (
	"testing"

	"blog-engine/internal/viewmodel"
)

// TestHomePageSizeBoundsMatchHandlerLimit 是 `home_page_size` 的**跨包一致性**守护。
//
// 三个包各自持有一个上界常量，语义必须完全一致：
//   - handler.maxPageSize（本包，所有列表端点的 page_size 硬上限）；
//   - viewmodel.MaxHomePageSize（HTTP 边界校验）;
//   - config.MaxHomePageSize（settings.json 的持久化校验）。
//
// 为什么必须钉住：首页发出的 `page_size` 就是站点设置里的 home_page_size，
// 而它要过列表端点的 parsePagination。**一旦允许的配置值超过 maxPageSize，
// 首页的第一次请求就会 400，整个站点白屏**——而且这个故障只在站长改过设置后
// 才出现，开发时永远看不到。三处常量分属不同包，改一处忘另一处的代价太大，
// 所以让它们在这里对撞。
//
// 这里只 import viewmodel（config 有独立的口径测试，见 config_test.go）：
// handler 包不 import config 是刻意的分层，不为测试破例。
func TestHomePageSizeBoundsMatchHandlerLimit(t *testing.T) {
	if viewmodel.MaxHomePageSize != maxPageSize {
		t.Fatalf("viewmodel.MaxHomePageSize = %d but handler.maxPageSize = %d: "+
			"a home page size above the list endpoint limit makes the very first "+
			"home page request fail with 400",
			viewmodel.MaxHomePageSize, maxPageSize)
	}
}

// TestHomePageSizeBoundsAreUsable 钉住区间本身是「可用的」而不是空的。
//
// 一个 Min > Max 的区间会让 Validate 拒绝一切输入（包括默认值），
// 而症状是「保存站点设置永远失败」——离真正的原因很远。
func TestHomePageSizeBoundsAreUsable(t *testing.T) {
	if viewmodel.MinHomePageSize < 1 {
		t.Fatalf("viewmodel.MinHomePageSize = %d, want at least 1", viewmodel.MinHomePageSize)
	}
	if viewmodel.MinHomePageSize > viewmodel.MaxHomePageSize {
		t.Fatalf("home page size bounds are inverted: min=%d max=%d",
			viewmodel.MinHomePageSize, viewmodel.MaxHomePageSize)
	}
}

// TestHomePageSizeDefaultFitsTheEndpointLimit 钉住默认值本身能过列表端点的护栏。
//
// 默认值越界与「配置值越界」是同一种故障（首页 400），但它不需要任何操作就会发生：
// 未配置过的站点直接用默认值。所以它单独占一条断言。
func TestHomePageSizeDefaultFitsTheEndpointLimit(t *testing.T) {
	if viewmodel.DefaultHomePageSize > maxPageSize {
		t.Fatalf("viewmodel.DefaultHomePageSize = %d exceeds handler.maxPageSize = %d: "+
			"every unconfigured site would fail its first home page request",
			viewmodel.DefaultHomePageSize, maxPageSize)
	}
	if viewmodel.DefaultHomePageSize < viewmodel.MinHomePageSize {
		t.Fatalf("viewmodel.DefaultHomePageSize = %d is below the allowed minimum %d",
			viewmodel.DefaultHomePageSize, viewmodel.MinHomePageSize)
	}
}

// TestBorderRadiusBoundsAreUsable 钉住圆角区间不是空的、而且**下界就是 0**。
//
// 与 home_page_size 最大的不同：这里没有「必须等于 handler.maxPageSize」的跨包约束
// （圆角越界不会让任何请求失败，上下界纯粹是审美判断），所以它没有上面对撞测试。
// 但反过来，圆角有两个别处没有的坑，都在这条断言里：
//
//   - Min > Max：区间反向会让 Validate 拒绝一切输入，症状是「保存站点设置永远失败」，
//     离真正的原因很远；
//   - Min > 0：把下界抬离 0 就等于取消「直角」这个选项，而界面上「0 为直角」的
//     说明还在——功能静默消失，没有任何报错。
func TestBorderRadiusBoundsAreUsable(t *testing.T) {
	if viewmodel.MinBorderRadius != 0 {
		t.Fatalf("viewmodel.MinBorderRadius = %d, want 0: a square corner must stay reachable, "+
			"otherwise the setting silently loses its \"0 = square\" option",
			viewmodel.MinBorderRadius)
	}
	if viewmodel.MinBorderRadius > viewmodel.MaxBorderRadius {
		t.Fatalf("border radius bounds are inverted: min=%d max=%d",
			viewmodel.MinBorderRadius, viewmodel.MaxBorderRadius)
	}
}

// TestBorderRadiusDefaultFitsBounds 钉住默认值落在区间内。
//
// 默认值越界与「配置值越界」的后果不同（不会 400），但更难发现：未配置过的站点
// 直接用默认值，而外壳下发的 data-border-radius 会是个非法值，前端的区间校验
// 再把它兜回它自己的 FALLBACK —— 于是「兜底」与「非法」互相抵消，页面看上去正常，
// 实际这个设置项被彻底忽略。
func TestBorderRadiusDefaultFitsBounds(t *testing.T) {
	if viewmodel.DefaultBorderRadius < viewmodel.MinBorderRadius ||
		viewmodel.DefaultBorderRadius > viewmodel.MaxBorderRadius {
		t.Fatalf("viewmodel.DefaultBorderRadius = %d is outside [%d, %d]: every unconfigured "+
			"site would ship an out-of-range data-border-radius and have it silently ignored",
			viewmodel.DefaultBorderRadius, viewmodel.MinBorderRadius, viewmodel.MaxBorderRadius)
	}
}
