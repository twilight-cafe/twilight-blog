package handler

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// 分页参数解析——所有列表端点的公共入口。
//
// 约定：page 与 page_size 都是必填，缺失即 400。这里刻意不给默认值，
// 目的是让分页行为显式可见，而不是让服务端静默兜底出「看起来能跑、
// 实则分页不对」的结果。page_size 另受 maxPageSize 上限约束。
// maxPageSize 是服务端强制的 page_size 上限，与前端传什么无关。
// 所有列表端点共用这个解析器，改这里等于改全部列表接口的护栏。
const maxPageSize = 12

// parsePagination 解析并校验 page / page_size。
//
// 返回的是普通 error（而非 service.ValidationError），调用方统一映射成 400
// 并把原文回给客户端；失败前会打 Debug 日志，便于定位前端的翻页问题。
// 页码从 1 开始，0 与负数一律视为非法。
func parsePagination(c echo.Context) (model.Pagination, error) {
	pageStr := c.QueryParam("page")
	if pageStr == "" {
		logging.Debugf("[handler] pagination missing page query")
		return model.Pagination{}, errors.New("page is required")
	}
	page, err := strconv.Atoi(pageStr)
	if err != nil || page < 1 {
		logging.Debugf("[handler] pagination invalid page=%q", pageStr)
		return model.Pagination{}, errors.New("page must be a positive integer")
	}
	// 上界不是「有没有那么多数据」的问题，而是**溢出**的问题：
	// Offset() 算的是 (page-1)*page_size，page 接近 int64 上限时这个乘法会回绕。
	// 仓储路径经由 Pagination.Normalized() 夹取，所以它一直是安全的；
	// 但搜索路径把 page 直接交给了 Meilisearch（client.go 里自己算 Offset），
	// 那里的回绕会让「第 2^62 页」摇身变成第 1 页，极端取值还会让
	// Meilisearch 收到负数 offset 而返回 400（表现为本站 500）。
	// 在这里一次性拦住，所有列表端点（含搜索）共用同一道闸。
	if page > model.MaxPage {
		logging.Debugf("[handler] pagination page too large page=%q", pageStr)
		return model.Pagination{}, fmt.Errorf("page must not exceed %d", model.MaxPage)
	}

	pageSizeStr := c.QueryParam("page_size")
	if pageSizeStr == "" {
		logging.Debugf("[handler] pagination missing page_size query")
		return model.Pagination{}, errors.New("page_size is required")
	}
	pageSize, err := strconv.Atoi(pageSizeStr)
	if err != nil || pageSize < 1 {
		logging.Debugf("[handler] pagination invalid page_size=%q", pageSizeStr)
		return model.Pagination{}, errors.New("page_size must be a positive integer")
	}
	if pageSize > maxPageSize {
		logging.Debugf("[handler] pagination page_size too large page_size=%q", pageSizeStr)
		return model.Pagination{}, errors.New("page_size must not exceed 12")
	}

	logging.Debugf("[handler] pagination parsed page=%d page_size=%d", page, pageSize)
	return model.Pagination{Page: page, PageSize: pageSize}, nil
}
