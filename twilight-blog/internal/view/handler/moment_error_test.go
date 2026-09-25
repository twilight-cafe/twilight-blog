package handler

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/service"
)

// mapMomentError 是「service 错误 → HTTP 状态」在动态写入路径上的唯一映射点。
//
// 与 articles 的 mapArticleError 分开测的原因：这两条路径**各自实现**，
// 动态这边历史上没有这个函数——Create 曾把任何错误一律映射成裸 500
// （"failed to create moment"），于是「客户端引用了一张已被删除的图」在运维
// 眼里和「数据库坏了」是同一条日志（实际上当时一条日志都没有）。
//
// 直呼而不走 HTTP 的理由与文章侧相同：default 分支（500）在端到端测试里到不了
// ——那需要让仓储在写库时真的失败，而 HTTP 层造得出来的失败全是可预期的 400。
// 恰恰是这条到不了的分支最需要被钉住：它一旦退化成「把内部错误当 200」或
// 「把客户端错误当 500」，前者是静默的数据损坏，后者是让调用方往错误方向排查。
func TestMapMomentError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{
			name:     "ValidationError → 400，消息原样透出（客户端要能据此改输入）",
			err:      &service.ValidationError{Message: "media 7 is video, but a moment attachment must be an image"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "media 7 is video, but a moment attachment must be an image",
		},
		{
			name:     "media 不存在 → 400，而不是 500（这正是 R6-01 修复前的行为）",
			err:      &service.ValidationError{Message: "media 7 does not exist"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "media 7 does not exist",
		},
		{
			name:     "未映射的错误 → 500，且只回固定文案",
			err:      errors.New("disk full: /var/lib/twilight/blog.db"),
			wantCode: http.StatusInternalServerError,
			// 内部细节（路径、SQL、驱动文案）绝不能进响应体：它们会经 toast 直接显示给用户。
			wantMsg: "failed to create moment",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assertMomentMappedStatus(t, testCase.err, "failed to create moment", testCase.wantCode, testCase.wantMsg)
		})
	}
}

// service 层用 %w 包装过的错误也必须被认出来。
// 断掉这条，Update 里任何一个加了上下文的包装都会把「配图不合法」变成 500——
// 用户选了一张已被删除的图，看到的是「服务器错误」而不是「那张图不存在了」。
func TestMapMomentErrorSeesThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("update moment 5: %w", &service.ValidationError{Message: "media 7 does not exist"})
	assertMomentMappedStatus(t, wrapped, "failed to update moment", http.StatusBadRequest, "media 7 does not exist")
}

func assertMomentMappedStatus(t *testing.T, err error, fallback string, wantCode int, wantMsg string) {
	t.Helper()

	var httpErr *echo.HTTPError
	if !errors.As(mapMomentError(err, fallback), &httpErr) {
		t.Fatalf("mapMomentError(%v) did not return *echo.HTTPError", err)
	}
	if httpErr.Code != wantCode {
		t.Fatalf("mapMomentError(%v) status = %d, want %d", err, httpErr.Code, wantCode)
	}
	message, ok := httpErr.Message.(string)
	if !ok {
		t.Fatalf("mapMomentError(%v) message has type %T, want string", err, httpErr.Message)
	}
	if message != wantMsg {
		t.Fatalf("mapMomentError(%v) message = %q, want %q", err, message, wantMsg)
	}
}
