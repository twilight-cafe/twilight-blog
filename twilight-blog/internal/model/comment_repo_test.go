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

func newTestCommentRepo(t *testing.T) *SQLiteCommentRepository {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "comments.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return NewSQLiteCommentRepository(db)
}

// addComment inserts a comment and returns its id.
func addComment(t *testing.T, repo *SQLiteCommentRepository, targetID int64, parentID *int64, content string, createdAt int64) int64 {
	t.Helper()

	comment := &Comment{
		AuthorType: "admin",
		AdminName:  "Admin",
		TargetType: "article",
		TargetID:   targetID,
		ParentID:   parentID,
		Content:    content,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}
	if err := repo.Create(context.Background(), comment); err != nil {
		t.Fatalf("create comment %q: %v", content, err)
	}
	return comment.ID
}

func commentIDs(comments []Comment) []int64 {
	ids := make([]int64, 0, len(comments))
	for _, comment := range comments {
		ids = append(ids, comment.ID)
	}
	return ids
}

// TestListByTargetBoundsTheDescendantFanOut 钉住评论树的读取上限。
//
// 回归的原始问题：递归 CTE 只对**根评论**分页，子孙阶段与最终 SELECT 都没有上限。
// 于是一条超深回复链能被一次**无需认证**的 GET 整体拉回——实测造 1 根 + 2999 层
// 回复后，page_size=12（甚至 page_size=1）都会返回 3000 条、耗时约 1.5 秒。
// 读取侧不需要任何凭据，所以这是纯粹的免费放大。
//
// 断言三件事：行数有上限、深度有上限、而且查询不会随链长线性变慢。
//
// 反证方式：把 maxCommentTreeDepth 与 maxCommentTreeRows 都改成极大值
// → 本条立刻因为返回 3000 条而失败。
func TestListByTargetBoundsTheDescendantFanOut(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()

	const targetID = 4242
	// 链长必须**远大于**任何合理的上限，而且断言要拿它当基准——
	// 如果拿 maxCommentTreeRows 当基准，把常量调大就等于把阈值也一起调大，
	// 测试会退化成同义反复（这一点是被反证实验抓出来的）。
	const chainLength = 600

	// 造一条尽可能深的链：每一层都是上一层的回复。
	var parent *int64
	for i := 0; i < chainLength; i++ {
		id := addComment(t, repo, targetID, parent, fmt.Sprintf("reply-%d", i), int64(1000+i))
		parent = &id
	}

	start := time.Now()
	comments, total, err := repo.ListByTarget(ctx, "article", targetID, nil, Pagination{Page: 1, PageSize: 12})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ListByTarget() error = %v", err)
	}

	if total != 1 {
		t.Fatalf("total = %d, want 1 (only one root comment exists)", total)
	}
	// 与常量无关的绝对断言：一条 600 层的链**必须**被截断。
	// 这条才是「上限真的生效了」的证据；下面两条只是核对上限的具体取值。
	if len(comments) >= chainLength {
		t.Fatalf("one anonymous request returned all %d comments of a deep chain: "+
			"the descendant fan-out is unbounded", len(comments))
	}
	if len(comments) > maxCommentTreeRows {
		t.Fatalf("returned %d rows, above the %d row cap", len(comments), maxCommentTreeRows)
	}
	if len(comments) > maxCommentTreeDepth {
		t.Fatalf("returned %d rows for a %d-deep chain, above the depth cap %d: "+
			"the recursive CTE is not depth-limited", len(comments), chainLength, maxCommentTreeDepth)
	}
	if len(comments) == 0 {
		t.Fatal("no comments were returned at all")
	}

	// 深度上限也应当让查询不再随链长增长。这里给一个宽松的上界，
	// 只为抓住「又退化成整链递归」这种情况，不作为性能基准。
	if elapsed > 2*time.Second {
		t.Fatalf("bounded tree query took %v; the recursion is probably unbounded again", elapsed)
	}
}

// TestListByTargetKeepsNormalThreadsIntact 是上面那条测试的对照：
// 上限不该伤到正常讨论——常见深度与条数必须原样返回。
func TestListByTargetKeepsNormalThreadsIntact(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()

	const targetID = 777
	root := addComment(t, repo, targetID, nil, "root", 1000)
	first := addComment(t, repo, targetID, &root, "first", 1001)
	second := addComment(t, repo, targetID, &first, "second", 1002)
	addComment(t, repo, targetID, &root, "sibling", 1003)
	otherRoot := addComment(t, repo, targetID, nil, "other root", 1004)
	addComment(t, repo, targetID, &otherRoot, "other reply", 1005)

	comments, total, err := repo.ListByTarget(ctx, "article", targetID, nil, Pagination{Page: 1, PageSize: 12})
	if err != nil {
		t.Fatalf("ListByTarget() error = %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(comments) != 6 {
		t.Fatalf("returned %d comments, want all 6 of a normal thread (ids=%v)", len(comments), commentIDs(comments))
	}
	_ = second
}

func equalIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestListByTargetPaginatesRootsAndKeepsSubtrees covers the recursive query that
// replaced loading every comment in memory: each page must contain whole reply
// subtrees even when the replies are not themselves paginated.
func TestListByTargetPaginatesRootsAndKeepsSubtrees(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()

	root1 := addComment(t, repo, 1, nil, "root-1", 100)
	root2 := addComment(t, repo, 1, nil, "root-2", 200)
	root3 := addComment(t, repo, 1, nil, "root-3", 300)

	// Two replies under root1, one nested deeper.
	reply1 := addComment(t, repo, 1, &root1, "reply-1", 150)
	addComment(t, repo, 1, &reply1, "reply-1-1", 160)
	addComment(t, repo, 1, &root2, "reply-2", 250)

	// A comment on a different target must never leak into this listing.
	addComment(t, repo, 2, nil, "other-target", 100)

	page1, total, err := repo.ListByTarget(ctx, "article", 1, nil, Pagination{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("ListByTarget page 1: %v", err)
	}
	if total != 3 {
		t.Fatalf("total root comments = %d, want 3", total)
	}

	// Roots are ordered by like count then age; all have zero likes here, so the
	// order follows created_at. Replies follow their parent depth-first.
	wantPage1 := []int64{root1, reply1, 0, root2, 0}
	got := commentIDs(page1)
	if len(got) != 5 {
		t.Fatalf("page 1 returned %d comments (%v), want 5", len(got), got)
	}
	// Verify parents appear before their children and roots are 1 then 2.
	if got[0] != wantPage1[0] || got[1] != wantPage1[1] || got[3] != wantPage1[3] {
		t.Fatalf("page 1 order = %v, want root1,reply1,reply1-1,root2,reply2", got)
	}

	page2, total2, err := repo.ListByTarget(ctx, "article", 1, nil, Pagination{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("ListByTarget page 2: %v", err)
	}
	if total2 != 3 {
		t.Fatalf("page 2 total = %d, want 3", total2)
	}
	if got2 := commentIDs(page2); !equalIDs(got2, []int64{root3}) {
		t.Fatalf("page 2 = %v, want [%d]", got2, root3)
	}
}

// TestListByTargetClampsPagination guards the repository-level bound: a negative
// page size means "no limit at all" in SQLite, so it must never reach SQL.
func TestListByTargetClampsPagination(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		addComment(t, repo, 1, nil, "root", int64(100+i))
	}

	comments, total, err := repo.ListByTarget(ctx, "article", 1, nil, Pagination{Page: -5, PageSize: -1})
	if err != nil {
		t.Fatalf("ListByTarget with invalid pagination: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	// DefaultPageSize (12) must apply, so all three rows come back rather than an
	// unbounded scan and rather than nothing.
	if len(comments) != 3 {
		t.Fatalf("returned %d comments, want 3", len(comments))
	}
}

// TestListAllExcludesSoftDeleted is the regression test for deleted comments
// (which carry the commenter's address) appearing in the admin list and count.
func TestListAllExcludesSoftDeleted(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()

	keep := addComment(t, repo, 1, nil, "keep", 100)
	drop := addComment(t, repo, 1, nil, "drop", 200)

	if _, err := repo.SoftDelete(ctx, []int64{drop}, 300); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	active, total, err := repo.ListAll(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if total != 1 {
		t.Fatalf("ListAll total = %d, want 1", total)
	}
	if got := commentIDs(active); !equalIDs(got, []int64{keep}) {
		t.Fatalf("ListAll ids = %v, want [%d]", got, keep)
	}

	deleted, deletedTotal, err := repo.ListDeleted(ctx, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListDeleted: %v", err)
	}
	if deletedTotal != 1 {
		t.Fatalf("ListDeleted total = %d, want 1", deletedTotal)
	}
	if got := commentIDs(deleted); !equalIDs(got, []int64{drop}) {
		t.Fatalf("ListDeleted ids = %v, want [%d]", got, drop)
	}
}

// TestNicknameRoundTrips makes sure the new column is read and written by every
// query path that renders a comment.
func TestNicknameRoundTrips(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()

	comment := &Comment{
		AuthorType: "admin",
		AdminName:  "Admin",
		Nickname:   "夜行者",
		TargetType: "article",
		TargetID:   1,
		Content:    "hello",
		CreatedAt:  100,
		UpdatedAt:  100,
	}
	if err := repo.Create(ctx, comment); err != nil {
		t.Fatalf("create: %v", err)
	}

	fetched, err := repo.GetByID(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fetched.Nickname != "夜行者" {
		t.Fatalf("GetByID nickname = %q, want 夜行者", fetched.Nickname)
	}

	list, _, err := repo.ListByTarget(ctx, "article", 1, nil, Pagination{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListByTarget: %v", err)
	}
	if len(list) != 1 || list[0].Nickname != "夜行者" {
		t.Fatalf("ListByTarget nicknames = %v, want [夜行者]", list)
	}
}

// seedLiker 插入一个账号，供 comment_likes.user_id 的外键使用。
func seedLiker(t *testing.T, repo *SQLiteCommentRepository, id int64) {
	t.Helper()

	if _, err := repo.db.ExecContext(context.Background(), `
INSERT INTO users (id, email, nickname, password_hash, created_at, updated_at)
VALUES (?, 'voter@example.com', 'voter', '', 1, 1)
`, id); err != nil {
		t.Fatalf("seed user %d: %v", id, err)
	}
}

func countLikes(t *testing.T, repo *SQLiteCommentRepository, commentID int64) int {
	t.Helper()

	var n int
	if err := repo.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM comment_likes WHERE comment_id = ?`, commentID).Scan(&n); err != nil {
		t.Fatalf("count comment_likes: %v", err)
	}
	return n
}

// TestHardDeleteLeavesActiveCommentUntouched 是「HardDelete 顺手删关联行」
// 这个缺陷在评论侧的回归测试。
//
// 旧实现在 DELETE FROM comments 之前先无条件执行了 DELETE FROM comment_likes。
// 对一条**未软删**的评论，第二句匹配不到行、评论还在，但第一句已经把它的点赞
// 记录清空了——一次「什么都没删掉」的调用却造成了数据损坏。
func TestHardDeleteLeavesActiveCommentUntouched(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()
	seedLiker(t, repo, 1)

	id := addComment(t, repo, 1, nil, "alive", 100)
	if err := repo.AddLike(ctx, id, 1, 150); err != nil {
		t.Fatalf("AddLike: %v", err)
	}
	if got := countLikes(t, repo, id); got != 1 {
		t.Fatalf("comment_likes rows = %d before HardDelete, want 1", got)
	}

	n, err := repo.HardDelete(ctx, []int64{id})
	if err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if n != 0 {
		t.Fatalf("HardDelete removed %d rows for an active comment, want 0", n)
	}

	if got := countLikes(t, repo, id); got != 1 {
		t.Fatalf("comment_likes rows = %d after HardDelete on an active comment, want 1", got)
	}
	fetched, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("active comment disappeared after HardDelete: %v", err)
	}
	if fetched.LikeCount != 1 {
		t.Fatalf("like_count = %d after HardDelete on an active comment, want 1", fetched.LikeCount)
	}
}

// TestHardDeleteOfDeletedCommentCascadesLikes 从另一侧守住同一个改动：
// 评论真的位于回收站时，点赞记录必须被清干净——清理现在完全依赖外键级联。
func TestHardDeleteOfDeletedCommentCascadesLikes(t *testing.T) {
	repo := newTestCommentRepo(t)
	ctx := context.Background()
	seedLiker(t, repo, 1)

	id := addComment(t, repo, 1, nil, "bye", 100)
	if err := repo.AddLike(ctx, id, 1, 150); err != nil {
		t.Fatalf("AddLike: %v", err)
	}
	if _, err := repo.SoftDelete(ctx, []int64{id}, 200); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	n, err := repo.HardDelete(ctx, []int64{id})
	if err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if n != 1 {
		t.Fatalf("HardDelete removed %d rows, want 1", n)
	}
	if got := countLikes(t, repo, id); got != 0 {
		t.Fatalf("comment_likes rows = %d after cascade, want 0", got)
	}
}

// ensure the sql import is used even if helpers change
var _ = sql.ErrNoRows
