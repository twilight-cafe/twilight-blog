package model

import (
	"context"
	"database/sql"
)

// 数据库驱动的底层结果接口在正常 SQLite 上几乎不会失败，但「失败时怎么办」
// 仍是仓储契约的一部分。把这些调用收成包级变量，测试才能在不破坏真实数据库的
// 前提下验证每一条错误返回，而不是留下永远无法执行的分支。
var (
	beginTxFn      = func(db *sql.DB, ctx context.Context) (*sql.Tx, error) { return db.BeginTx(ctx, nil) }
	lastInsertIDFn = func(res sql.Result) (int64, error) { return res.LastInsertId() }
	commitTxFn     = func(tx *sql.Tx) error { return tx.Commit() }
	scanValuesFn   = func(s scanner, dest ...any) error { return s.Scan(dest...) }
	rowsErrFn      = func(rows *sql.Rows) error { return rows.Err() }
)

// rowsAffectedFn 定义在 media_repo.go 里（那里最先需要它）；同一个变量被所有
// 仓储复用，避免每个文件各建一套同名钩子。
