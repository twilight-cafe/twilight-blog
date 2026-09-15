package handler

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/model"
	"blog-engine/internal/service"
)

// mapArticleError 是「service 错误 → HTTP 状态」在文章写入路径上的唯一映射点。
//
// 为什么在这里直呼而不是走 HTTP：它的 default 分支（500）在 view 包的端到端测试里
// 到不了——要走到那里得让仓储在写库时真的失败，而那层测试没有可用的故障注入点
// （HTTP 层能造出来的失败全是 400/404 这两类可预期的）。恰恰是这条到不了的分支
// 最需要被钉住：它一旦退化成「把内部错误当 200」或「panic」，前一条是静默的数据
// 损坏，后一条是整个进程掉线，而两者都不会被 400/404 的用例发现。
func TestMapArticleError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{
			name:     "ValidationError → 400，消息原样透出（客户端要能据此改输入）",
			err:      &service.ValidationError{Message: "cover media 7 is audio, but a cover must be an image"},
			wantCode: http.StatusBadRequest,
			wantMsg:  "cover media 7 is audio, but a cover must be an image",
		},
		{
			name:     "ErrNotFound → 404",
			err:      model.ErrNotFound,
			wantCode: http.StatusNotFound,
			wantMsg:  "article not found",
		},
		{
			name:     "未映射的错误 → 500，且只回固定文案",
			err:      errors.New("disk full: /var/lib/twilight/blog.db"),
			wantCode: http.StatusInternalServerError,
			// 内部细节（路径、SQL、驱动文案）绝不能进响应体：它们会经 toast 直接显示给用户。
			wantMsg: "failed to create article",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := mapArticleError(testCase.err, "failed to create article")

			var httpErr *echo.HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("mapArticleError() = %T (%v), want *echo.HTTPError", err, err)
			}
			if httpErr.Code != testCase.wantCode {
				t.Fatalf("status = %d, want %d", httpErr.Code, testCase.wantCode)
			}
			message, ok := httpErr.Message.(string)
			if !ok {
				t.Fatalf("message has type %T, want string", httpErr.Message)
			}
			if message != testCase.wantMsg {
				t.Fatalf("message = %q, want %q", message, testCase.wantMsg)
			}
		})
	}
}

// service 层用 %w 包装过的错误也必须被认出来。
// 断掉这条，`Update` 里任何一个加了上下文的包装都会把「头图不合法」变成 500——
// 用户提交了一张音频当封面，看到的是「服务器错误」而不是「头图必须是图片」。
func TestMapArticleErrorSeesThroughWrapping(t *testing.T) {
	wrappedValidation := fmt.Errorf("update article 5: %w", &service.ValidationError{Message: "cover media 7 is audio"})
	assertMappedStatus(t, wrappedValidation, "failed to update article", http.StatusBadRequest, "cover media 7 is audio")

	wrappedNotFound := fmt.Errorf("update article 5: %w", model.ErrNotFound)
	assertMappedStatus(t, wrappedNotFound, "failed to update article", http.StatusNotFound, "article not found")
}

func assertMappedStatus(t *testing.T, err error, fallback string, wantCode int, wantMsg string) {
	t.Helper()

	var httpErr *echo.HTTPError
	if !errors.As(mapArticleError(err, fallback), &httpErr) {
		t.Fatalf("mapArticleError(%v) did not return *echo.HTTPError", err)
	}
	if httpErr.Code != wantCode {
		t.Fatalf("mapArticleError(%v) status = %d, want %d", err, httpErr.Code, wantCode)
	}
	if httpErr.Message != wantMsg {
		t.Fatalf("mapArticleError(%v) message = %v, want %q", err, httpErr.Message, wantMsg)
	}
}
