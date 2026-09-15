package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"blog-engine/internal/database"
	"blog-engine/internal/model"
)

// newTestCommentService 用真实 SQLite 仓储组装评论服务。
//
// 这里刻意不用手写的假仓储：评论署名的来源是「评论表里那一列」，
// 而假仓储很容易把被测代码写下的值原样吐回来，于是无论生产代码写对了
// 还是写错了，测试都能通过——那正是本次要防的那类空洞测试。
//
// 同时预置三类可评论目标各一条（id 都是 1）。评论现在要求目标真实存在
// （见 CommentService.validateTargetExists），不预置的话，每个用例都会
// 先撞在「目标不存在」上，把真正的被测行为挡在后面。
func newTestCommentService(t *testing.T) (*CommentService, *sql.DB) {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "comments.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := model.NewSQLiteArticleRepository(db).Create(ctx, &model.Article{
		Title: "target article", Content: "body", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed target article: %v", err)
	}
	if err := model.NewSQLiteMomentRepository(db).Create(ctx, &model.Moment{
		Content: "target moment", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed target moment: %v", err)
	}
	if err := model.NewSQLitePageRepository(db).Create(ctx, &model.Page{
		Title: "target page", Content: "body", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed target page: %v", err)
	}

	svc := NewCommentService(
		model.NewSQLiteCommentRepository(db),
		model.NewSQLiteUserRepository(db),
		NewCommentTargetLookups(
			model.NewSQLiteArticleRepository(db),
			model.NewSQLiteMomentRepository(db),
			model.NewSQLitePageRepository(db),
		),
	)
	return svc, db
}

// seedCommentUser 直接写入一行账号，返回其 id。
//
// 不走 UserService.Register：本测试只关心评论的署名取自账号资料，
// 验证码、密码强度那些前置条件与它无关，混进来只会让失败原因变得难读。
func seedCommentUser(t *testing.T, db *sql.DB, email, username, nickname string) int64 {
	t.Helper()

	repo := model.NewSQLiteUserRepository(db)
	user := &model.User{Email: email, Username: username, Nickname: nickname, CreatedAt: 1, UpdatedAt: 1}
	if err := repo.Create(context.Background(), user); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	return user.ID
}

// TestCommentAuthorNameComesFromTheAccount 守护的是一条契约：
// **评论署名只能来自账号资料，不能来自请求**。
//
// 所以这里的断言刻意做了两件事：既检查署名等于账号的昵称，
// 也检查它**不等于**账号的用户名——只断言前者的话，一个「把用户名
// 当昵称用」的实现（正是本项目历史上犯过的错）照样能通过。
func TestCommentAuthorNameComesFromTheAccount(t *testing.T) {
	svc, db := newTestCommentService(t)
	ctx := context.Background()

	userID := seedCommentUser(t, db, "night@example.com", "night-walker", "夜行者")

	comment, err := svc.CreateAsUser(ctx, userID, "article", 1, nil, "hello")
	if err != nil {
		t.Fatalf("CreateAsUser() error = %v", err)
	}

	if comment.Nickname != "夜行者" {
		t.Fatalf("comment nickname = %q, want 夜行者", comment.Nickname)
	}
	if comment.Nickname == "night-walker" {
		t.Fatalf("comment nickname fell back to the login name %q", comment.Nickname)
	}
}

// TestCommentAuthorNameFollowsLaterAccountRenames 说明署名不是「发表那一刻拷贝
// 一份永久固定」，而是跟着账号资料走——改名之后旧评论的署名也应当是新的，
// 否则同一个人会在同一篇文章下顶着两个名字。
func TestCommentAuthorNameFollowsLaterAccountRenames(t *testing.T) {
	svc, db := newTestCommentService(t)
	ctx := context.Background()

	userID := seedCommentUser(t, db, "night@example.com", "night-walker", "旧名")

	first, err := svc.CreateAsUser(ctx, userID, "article", 1, nil, "hello")
	if err != nil {
		t.Fatalf("CreateAsUser() error = %v", err)
	}
	if first.Nickname != "旧名" {
		t.Fatalf("comment nickname = %q, want 旧名", first.Nickname)
	}

	if err := model.NewSQLiteUserRepository(db).UpdateNickname(ctx, userID, "新名", 2); err != nil {
		t.Fatalf("UpdateNickname() error = %v", err)
	}

	second, err := svc.CreateAsUser(ctx, userID, "article", 1, nil, "hello again")
	if err != nil {
		t.Fatalf("CreateAsUser() error = %v", err)
	}
	if second.Nickname != "新名" {
		t.Fatalf("comment nickname after rename = %q, want 新名", second.Nickname)
	}
}

// TestCommentAuthorNameNeverLeaksTheEmail 覆盖昵称为空的历史账号：
// 兜底名必须是固定的匿名名，绝不能从邮箱推出来（那会泄露半个私人地址）。
func TestCommentAuthorNameNeverLeaksTheEmail(t *testing.T) {
	svc, db := newTestCommentService(t)
	ctx := context.Background()

	userID := seedCommentUser(t, db, "visitor@example.com", "visitor", "")

	comment, err := svc.CreateAsUser(ctx, userID, "article", 1, nil, "hello")
	if err != nil {
		t.Fatalf("CreateAsUser() error = %v", err)
	}

	if comment.Nickname != model.AnonymousDisplayName {
		t.Fatalf("nickname = %q, want %q", comment.Nickname, model.AnonymousDisplayName)
	}
	if strings.Contains(comment.Nickname, "visitor") {
		t.Fatalf("nickname %q leaks the email local part", comment.Nickname)
	}
}

// TestCommentNicknameIsCleanedFromTheAccount 说明账号昵称里的换行与填充空白
// 会被折叠后再写进评论行——评论列表与日志都靠这一个字段取值。
func TestCommentNicknameIsCleanedFromTheAccount(t *testing.T) {
	svc, db := newTestCommentService(t)
	ctx := context.Background()

	userID := seedCommentUser(t, db, "messy@example.com", "messy", "  alice\n\tbob  ")

	comment, err := svc.CreateAsUser(ctx, userID, "article", 1, nil, "hello")
	if err != nil {
		t.Fatalf("CreateAsUser() error = %v", err)
	}
	if comment.Nickname != "alice bob" {
		t.Fatalf("nickname = %q, want %q", comment.Nickname, "alice bob")
	}
}

// TestCommentFromUnknownAccountIsRejected 覆盖「会话里的 user_id 指向一个已被
// 删除的账号」：这种情况必须报错，而不是插一条署名为空的评论。
func TestCommentFromUnknownAccountIsRejected(t *testing.T) {
	svc, _ := newTestCommentService(t)
	ctx := context.Background()

	if _, err := svc.CreateAsUser(ctx, 4242, "article", 1, nil, "hello"); err == nil {
		t.Fatal("a comment from a non-existent account was accepted")
	}
}

// countComments 直接数评论表的行数。用于断言「被拒绝的写入真的没有落库」——
// 只断言返回了错误是不够的：一个先写库再校验的实现同样会返回错误。
func countComments(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM comments").Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	return n
}

// TestCommentOnMissingTargetIsRejected 守护 R3-10：评论目标必须真实存在。
//
// 修复前，「文章 999999」这种不存在的 id 会被接受（201），评论落库并可以被
// 匿名读出来——于是任何人都能用不存在的 id 无限灌内容，而且这些内容与
// 任何生命周期管理（删除文章）都脱钩。
func TestCommentOnMissingTargetIsRejected(t *testing.T) {
	svc, db := newTestCommentService(t)
	ctx := context.Background()

	if _, err := svc.CreateAsAdmin(ctx, "admin", "article", 999999, nil, "ghost"); err == nil {
		t.Fatal("a comment on a non-existent article was accepted")
	} else if !errors.Is(err, model.ErrCommentTargetNotFound) {
		t.Fatalf("error = %v, want ErrCommentTargetNotFound", err)
	}
	if n := countComments(t, db); n != 0 {
		t.Fatalf("comments written = %d, want 0 (the rejected write must not reach the table)", n)
	}
}

// TestListByTargetRejectsAnAbsentTarget 保证读取侧同样收缩：
// 目标不存在时不能再返回 200 空列表，否则「文章已删除」与「刚发布还没人评论」
// 在前端看起来完全一样。
func TestListByTargetRejectsAnAbsentTarget(t *testing.T) {
	svc, _ := newTestCommentService(t)
	ctx := context.Background()

	_, _, err := svc.ListByTarget(ctx, "article", 999999, nil, model.Pagination{Page: 1, PageSize: 12})
	if !errors.Is(err, model.ErrCommentTargetNotFound) {
		t.Fatalf("ListByTarget error = %v, want ErrCommentTargetNotFound", err)
	}
}

// TestSoftDeletedTargetHidesItsComments 覆盖内容生命周期：
// 文章被软删之后，它下面的评论**既不能再被写入，也不能再被匿名读出**。
// 修复前这两件事都成立——文章 404 了，评论却还在。
func TestSoftDeletedTargetHidesItsComments(t *testing.T) {
	svc, db := newTestCommentService(t)
	ctx := context.Background()

	userID := seedCommentUser(t, db, "gone@example.com", "gone", "消失的作者")
	if _, err := svc.CreateAsUser(ctx, userID, "article", 1, nil, "before the delete"); err != nil {
		t.Fatalf("CreateAsUser() before delete error = %v", err)
	}

	affected, err := model.NewSQLiteArticleRepository(db).SoftDelete(ctx, []int64{1}, 2)
	if err != nil || affected != 1 {
		t.Fatalf("SoftDelete() affected=%d err=%v, want 1 and nil", affected, err)
	}

	if _, _, err := svc.ListByTarget(ctx, "article", 1, nil, model.Pagination{Page: 1, PageSize: 12}); !errors.Is(err, model.ErrCommentTargetNotFound) {
		t.Fatalf("ListByTarget on a soft-deleted article error = %v, want ErrCommentTargetNotFound", err)
	}
	if _, err := svc.CreateAsUser(ctx, userID, "article", 1, nil, "after the delete"); !errors.Is(err, model.ErrCommentTargetNotFound) {
		t.Fatalf("CreateAsUser on a soft-deleted article error = %v, want ErrCommentTargetNotFound", err)
	}
	// 评论行本身仍在（软删文章不等于删除评论），只是不再可达。
	if n := countComments(t, db); n != 1 {
		t.Fatalf("comments rows = %d, want 1 (deleting an article must not delete comments)", n)
	}
}

// TestUnwiredTargetLookupFailsClosed 守护「漏接线」这一类故障：
// 没有装配目标查询函数时，写入必须**失败**而不是跳过校验。
//
// 这一条是针对实现方式本身的：如果 validateTargetExists 在查不到 watcher 时
// 直接 return nil，那么这个安全校验就会因为「构造时少传一个参数」而静默消失，
// 而所有既有测试仍然全绿——正是本项目最忌讳的静默失效。
func TestUnwiredTargetLookupFailsClosed(t *testing.T) {
	_, db := newTestCommentService(t)
	ctx := context.Background()

	unwired := NewCommentService(
		model.NewSQLiteCommentRepository(db),
		model.NewSQLiteUserRepository(db),
		map[string]CommentTargetLookup{},
	)
	if _, err := unwired.CreateAsAdmin(ctx, "admin", "article", 1, nil, "must not be written"); err == nil {
		t.Fatal("a service without a target lookup accepted a comment write")
	}
	if n := countComments(t, db); n != 0 {
		t.Fatalf("comments written = %d, want 0", n)
	}
}
