package model

// DefaultPageSize 是调用方未指定页大小时的默认值。
const DefaultPageSize = 12

// MaxPageSize 限制单次查询最多返回多少行。handler 层还有更严的对外上限；
// 这里是仓储层兜底，保证内部调用方也无法要求一个无界的结果集。
const MaxPageSize = 100

// MaxPage 限制页码，使 Offset() 不会溢出。超过该值的页码早已远离任何真实数据量，
// 结果只会是空列表。
const MaxPage = 1_000_000

type Pagination struct {
	Page     int
	PageSize int
}

// Normalized 把越界取值夹到安全区间后返回。
//
// 所有仓储查询都经由 Limit()/Offset() 取值，而这两者会先做归一化，
// 因此负数或缺省的页大小不会抵达 SQL。SQLite 里负的 LIMIT 意为「完全不限制」，
// 所以这里不只关乎内存占用，也关乎正确性。
func (p Pagination) Normalized() Pagination {
	page := p.Page
	if page < 1 {
		page = 1
	}
	if page > MaxPage {
		page = MaxPage
	}

	size := p.PageSize
	if size < 1 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}

	return Pagination{Page: page, PageSize: size}
}

func (p Pagination) Offset() int {
	n := p.Normalized()
	return (n.Page - 1) * n.PageSize
}

func (p Pagination) Limit() int {
	return p.Normalized().PageSize
}
