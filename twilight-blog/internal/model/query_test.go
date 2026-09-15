package model

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"blog-engine/internal/database"
)

// openTestDB 打开一个临时数据库（含完整 schema 与 pragma），供本包各测试复用。
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "model.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestReadTxHoldsTheOnlyConnection 钉住 readTx 的两条契约，二者共同构成
// 「total 与当页处于同一快照」这个保证：
//
//  1. 回调拿到的确实是事务句柄，而不是连接池——否则两条查询各自成句，
//     快照就又不一致了。这里直接对 q 做类型断言，因为从查询结果上无法区分；
//  2. 整个回调期间事务确实开着：连接池被限制为 1 条（database.Open 里的
//     SetMaxOpenConns(1)），所以事务持有时，任何其它语句都必须等待。
//     回调返回后写入应当顺利完成——被延后，而不是被永久卡住。
//
// 契约 2 顺带守护一个更凶的后果：既然只读事务会独占连接，那么**在已有事务里
// 再调 readTx 就会死锁**。本包之外不开启事务这条约定（service/handler 只调
// 仓储方法）一旦被破坏，症状是整个进程卡死，而不是报错。
//
// 整个断言包在 5 秒超时里：readTx 一旦被改成「开着事务却把连接池交给回调」，
// 回调里的查询会永久阻塞。此时若直接等待，测试会挂到 go test 的全局超时，
// 拿不到任何有用输出；包一层超时就能得到一句说清原因的失败。
func TestReadTxHoldsTheOnlyConnection(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// 只阻塞 100ms 就足以判定「写没写进去」，不必真的等它完成。
	const blockedWindow = 100 * time.Millisecond

	type outcome struct {
		contract error // 契约 1、2 的失败
		after    error // readTx 返回后那次写入的结果
	}
	done := make(chan outcome, 1)

	go func() {
		var out outcome
		writeDone := make(chan error, 1)
		writeStarted := false

		out.contract = readTx(ctx, db, func(ctx context.Context, q queryRunner) error {
			if _, ok := q.(*sql.Tx); !ok {
				return fmt.Errorf("callback received %T, want *sql.Tx", q)
			}

			// 事务内必须能正常读到 schema 里的表。
			var name string
			if err := q.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE name = 'articles'`).Scan(&name); err != nil {
				return fmt.Errorf("read inside readTx: %w", err)
			}

			writeStarted = true
			go func() {
				// 另起一个连接去写。连接池只有 1 条，所以它必须等到事务结束。
				_, err := db.ExecContext(context.Background(), `CREATE TABLE readtx_probe (x INTEGER)`)
				writeDone <- err
			}()

			select {
			case err := <-writeDone:
				return fmt.Errorf("concurrent write went through while readTx held the transaction (err = %v)", err)
			case <-time.After(blockedWindow):
				return nil
			}
		})

		// 契约 1 若已不成立，就没必要再等那次写入的观察结果了——
		// 它根本没被发起，白等只会把失败拖成超时。
		if !writeStarted {
			done <- out
			return
		}

		// 事务结束后写入应当顺利完成——被延后，而不是被永久卡住。
		select {
		case out.after = <-writeDone:
		case <-time.After(5 * time.Second):
			out.after = fmt.Errorf("write never completed; the transaction was not released")
		}
		done <- out
	}()

	select {
	case out := <-done:
		if out.contract != nil {
			t.Fatalf("readTx violated its contract: %v", out.contract)
		}
		if out.after != nil {
			t.Fatalf("write after readTx: %v", out.after)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readTx did not return within 5s, so a query inside the callback is blocked: " +
			"readTx opened a transaction (holding the single pooled connection) but handed the callback " +
			"the pool instead of the transaction")
	}
}
