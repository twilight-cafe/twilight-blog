package model

import (
	"context"
	"database/sql"
	"fmt"
)

// queryRunner 抽象出 *sql.DB 与 *sql.Tx 共有的只读方法，让同一段查询既能直接
// 走连接池，也能放进事务里执行。
//
// 存在的理由：装配关联数据的那几个辅助函数（loadTagsForArticles、
// loadMediaForMoments）既要能从事务内调用（列表方法要保持快照一致），
// 也要能被 GetByID 直接用连接池调用。把参数写成 queryRunner，两种调用方都能传。
type queryRunner interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readTx 在一个事务内执行 fn，让「总数 + 当页 + 关联数据」落在同一个快照上。
//
// 为什么需要它：列表方法要同时返回 total 与当页数据。若两条查询各自走连接池，
// 两次读取之间可能插入别的 goroutine 的写入（新建或删除内容会同时改变 COUNT
// 与分页的偏移量），于是出现「total 说有 13 条、当页却按 12 条的偏移量取出」
// 这类自相矛盾的结果。SQLite 在事务内提供可重复读，把整段读放进同一个事务
// 即可消除。
//
// 两个实现细节：
//   - 用 BeginTx(ctx, nil) 而不是 ReadOnly: true —— 事务内只跑 SELECT 时
//     SQLite 的 BEGIN 本就是个读事务，不必依赖驱动对 TxOptions 的支持；
//   - 结束时用 Rollback 释放而不是 Commit —— 本函数不写任何东西，
//     回滚与提交在效果上等价，而 Rollback 是 defer 里更安全的那个。
//
// 关于自锁：本包之外没有任何地方开启事务（service / handler 只调仓储方法），
// 所以不存在「已持有事务再开一个」的情形。这一点必须保持——连接池被限制为
// 1 条连接（见 database.Open），一旦嵌套就会直接死锁。
func readTx(ctx context.Context, db *sql.DB, fn func(ctx context.Context, q queryRunner) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin read transaction: %w", err)
	}
	defer tx.Rollback()

	return fn(ctx, tx)
}
