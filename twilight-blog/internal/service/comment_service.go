package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// allowedCommentTargets 是评论 target_type 的白名单：评论只能挂在这三类内容下。
// 其它取值一律按「目标非法」拒绝——不要放宽成「白名单之外也算合法」的判断。
var allowedCommentTargets = map[string]struct{}{
	"article": {},
	"moment":  {},
	"page":    {},
}

// CommentTargetLookup 判定一个评论目标「存在且未软删」。
//
// 返回 (exists, err)：exists=false 且 err=nil 表示目标确实不存在，
// err 非 nil 表示查询本身失败（数据库错误等）。把这两种结局分开，
// 是为了不让一次数据库故障伪装成「内容不存在」。
type CommentTargetLookup func(ctx context.Context, id int64) (bool, error)

// CommentService 处理评论的业务规则：两种作者身份、树形回复、点赞、软删与回收站。
//
// 评论有两个写入口：CreateAsUser（已登录的访客账号）与 CreateAsAdmin（站长）。
// 两者的展示名都不来自请求体：访客用账号的 nickname，站长用配置里的展示名。
// 调用方无法为自己指定一个临时署名——署名只有一个来源，就是身份本身。
//
// targets 让「评论只能挂在真实存在的内容下」这条规则有个执行点。它按
// target_type 索引，而不是让本包直接依赖三个内容仓储：评论与那三类内容
// 之间没有别的关系，收成一张查询表后，新增一类可评论内容只需要多接一个函数。
type CommentService struct {
	repo    model.CommentRepository
	users   model.UserRepository
	targets map[string]CommentTargetLookup
}

func NewCommentService(repo model.CommentRepository, users model.UserRepository, targets map[string]CommentTargetLookup) *CommentService {
	return &CommentService{repo: repo, users: users, targets: targets}
}

// NewCommentTargetLookups 把三个内容仓储收敛成「目标是否可见」的查询函数。
//
// 三个仓储各有自己的 not-found sentinel（ErrNotFound / ErrMomentNotFound /
// ErrPageNotFound），翻译在这里做一次，调用点就不必重复那串三选一的 errors.Is。
// 三者的 GetByID 都带 deleted_at IS NULL，因此「软删掉的文章」在这里自然
// 表现为不存在——这正是我们要的语义：内容下架之后，它的评论也不该再被公开读取。
func NewCommentTargetLookups(articles model.ArticleRepository, moments model.MomentRepository, pages model.PageRepository) map[string]CommentTargetLookup {
	return map[string]CommentTargetLookup{
		"article": func(ctx context.Context, id int64) (bool, error) {
			_, err := articles.GetByID(ctx, id)
			return targetExists(err, model.ErrNotFound)
		},
		"moment": func(ctx context.Context, id int64) (bool, error) {
			_, err := moments.GetByID(ctx, id)
			return targetExists(err, model.ErrMomentNotFound)
		},
		"page": func(ctx context.Context, id int64) (bool, error) {
			_, err := pages.GetByID(ctx, id)
			return targetExists(err, model.ErrPageNotFound)
		},
	}
}

// targetExists 把「取不到那一行」翻译成 (false, nil)，其余错误原样上抛。
func targetExists(err error, notFound error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, notFound):
		return false, nil
	default:
		return false, err
	}
}

// validateTargetExists 要求评论目标必须存在且未软删，否则 ErrCommentTargetNotFound。
//
// 找不到查询函数时**拒绝写入而不是放行**：漏接线属于编码错误，若静默跳过校验，
// 表现会是「评论能挂到任何 id 上」，而那正是这条校验要堵的口子。宁可 500 也不 fail open。
func (s *CommentService) validateTargetExists(ctx context.Context, targetType string, targetID int64) error {
	lookup, ok := s.targets[targetType]
	if !ok || lookup == nil {
		logging.Errorf("[comment] no target lookup wired for target_type=%q; refusing the write", targetType)
		return fmt.Errorf("comment target validation is not configured for %q", targetType)
	}
	exists, err := lookup(ctx, targetID)
	if err != nil {
		logging.Errorf("[comment] target lookup failed target_type=%s target_id=%d: %v", targetType, targetID, err)
		return err
	}
	if !exists {
		return model.ErrCommentTargetNotFound
	}
	return nil
}

// CreateAsUser 以访客账号身份创建评论，展示名取该账号的昵称。
//
// 昵称**只从账号资料里读**，不接受调用方传入：让每次评论都能自取一个名字，
// 等于把「显示名」变成两套（账号一套、评论一套），而这两套迟早会不一致。
// 账号资料里没有昵称（历史数据）时兜底为匿名名，与 viewmodel 层的兜底一致。
//
// 除内容校验外还会校验 parentID：回复必须挂在与自身同一目标下的评论上，
// 不会出现「A 文章的回复跑到 B 文章下」这种跨目标错挂。
func (s *CommentService) CreateAsUser(ctx context.Context, userID int64, targetType string, targetID int64, parentID *int64, content string) (*model.Comment, error) {
	content = strings.TrimSpace(content)
	logging.Debugf("[comment] creating user comment user_id=%d target_type=%s target_id=%d parent_id=%v content_len=%d", userID, targetType, targetID, parentID, len(content))

	author, err := s.users.GetByID(ctx, userID)
	if err != nil {
		logging.Errorf("[comment] load comment author failed user_id=%d: %v", userID, err)
		return nil, err
	}
	nickname := normalizeNickname(author.Nickname)
	if nickname == "" {
		nickname = model.AnonymousDisplayName
	}

	if err := validateCommentInput(targetType, targetID, content, nickname, true); err != nil {
		return nil, err
	}
	if err := s.validateTargetExists(ctx, targetType, targetID); err != nil {
		return nil, err
	}
	if err := s.validateParent(ctx, parentID, targetType, targetID); err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	comment := &model.Comment{
		AuthorType: "user",
		UserID:     int64Ptr(userID),
		Nickname:   nickname,
		TargetType: targetType,
		TargetID:   targetID,
		ParentID:   parentID,
		Content:    content,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := s.repo.Create(ctx, comment); err != nil {
		logging.Errorf("[comment] create user comment failed user_id=%d target_type=%s target_id=%d: %v", userID, targetType, targetID, err)
		return nil, err
	}

	created, err := s.repo.GetByID(ctx, comment.ID)
	if err != nil {
		logging.Errorf("[comment] load created user comment failed id=%d: %v", comment.ID, err)
		return nil, err
	}
	logging.Infof("[comment] created id=%d user_id=%d target_type=%s target_id=%d", created.ID, userID, targetType, targetID)
	return created, nil
}

// CreateAsAdmin 以站长身份创建评论。昵称为空时兜底为 "Admin"。
// 注意传给 validateCommentInput 的 nicknameRequired 是 false——站长不必填昵称。
func (s *CommentService) CreateAsAdmin(ctx context.Context, adminName, targetType string, targetID int64, parentID *int64, content string) (*model.Comment, error) {
	content = strings.TrimSpace(content)
	adminName = strings.TrimSpace(adminName)
	if adminName == "" {
		adminName = "Admin"
	}
	logging.Debugf("[comment] creating admin comment target_type=%s target_id=%d parent_id=%v content_len=%d", targetType, targetID, parentID, len(content))
	if err := validateCommentInput(targetType, targetID, content, "", false); err != nil {
		return nil, err
	}
	if err := s.validateTargetExists(ctx, targetType, targetID); err != nil {
		return nil, err
	}
	if err := s.validateParent(ctx, parentID, targetType, targetID); err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	comment := &model.Comment{
		AuthorType: "admin",
		AdminName:  adminName,
		TargetType: targetType,
		TargetID:   targetID,
		ParentID:   parentID,
		Content:    content,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := s.repo.Create(ctx, comment); err != nil {
		logging.Errorf("[comment] create admin comment failed target_type=%s target_id=%d: %v", targetType, targetID, err)
		return nil, err
	}
	created, err := s.repo.GetByID(ctx, comment.ID)
	if err != nil {
		logging.Errorf("[comment] load created admin comment failed id=%d: %v", comment.ID, err)
		return nil, err
	}
	logging.Infof("[comment] created id=%d target_type=%s target_id=%d", created.ID, targetType, targetID)
	return created, nil
}

// ListByTarget 返回一页评论，形式是「深度优先排序后的扁平切片」
// （每条回复紧跟在它的父评论之后），外加根评论总数。
//
// 分页在 SQL 里完成，本方法只对拿到的行重排，因此开销不再随评论总量增长。
// 校验：targetType 必须在白名单内且 targetID > 0，否则 ErrInvalidCommentTarget；
// 另外还要求目标内容**存在且未软删**（否则 ErrCommentTargetNotFound）——
// 文章被删除后，它的评论不该继续被匿名读出来。
func (s *CommentService) ListByTarget(ctx context.Context, targetType string, targetID int64, currentUserID *int64, p model.Pagination) ([]model.Comment, int64, error) {
	if _, ok := allowedCommentTargets[targetType]; !ok {
		return nil, 0, model.ErrInvalidCommentTarget
	}
	if targetID <= 0 {
		return nil, 0, model.ErrInvalidCommentTarget
	}
	if err := s.validateTargetExists(ctx, targetType, targetID); err != nil {
		return nil, 0, err
	}
	logging.Debugf("[comment] list target_type=%s target_id=%d page=%d page_size=%d", targetType, targetID, p.Page, p.PageSize)

	comments, total, err := s.repo.ListByTarget(ctx, targetType, targetID, currentUserID, p)
	if err != nil {
		logging.Errorf("[comment] list by target failed target_type=%s target_id=%d: %v", targetType, targetID, err)
		return nil, 0, err
	}
	return orderCommentTree(comments), total, nil
}

// orderCommentTree 把根评论与回复整理成展示顺序：根评论先按点赞数降序、
// 再按创建时间升序；每条根评论后面深度优先跟着它的回复（回复之间按时间升序）。
//
// 关键容错：若某条回复的父评论不在本次数据里（父评论已被软删，或被分页切到了别页），
// 这条回复会被提升为根评论而不是被丢弃——任何评论都不会从这里静默消失。
// visited 集合用于防止异常数据中的环导致无限递归。
func orderCommentTree(comments []model.Comment) []model.Comment {
	if len(comments) == 0 {
		return []model.Comment{}
	}

	presentIDs := make(map[int64]bool, len(comments))
	for i := range comments {
		presentIDs[comments[i].ID] = true
	}

	childrenByParent := make(map[int64][]model.Comment)
	roots := make([]model.Comment, 0, len(comments))
	for i := range comments {
		comment := comments[i]
		parentID := comment.ParentID
		// A reply whose parent is missing from this page (soft-deleted parent)
		// is promoted to a root so it is never silently dropped.
		if parentID == nil || !presentIDs[*parentID] {
			roots = append(roots, comment)
			continue
		}
		childrenByParent[*parentID] = append(childrenByParent[*parentID], comment)
	}

	sort.SliceStable(roots, func(i, j int) bool {
		if roots[i].LikeCount != roots[j].LikeCount {
			return roots[i].LikeCount > roots[j].LikeCount
		}
		if roots[i].CreatedAt != roots[j].CreatedAt {
			return roots[i].CreatedAt < roots[j].CreatedAt
		}
		return roots[i].ID < roots[j].ID
	})

	for parentID := range childrenByParent {
		group := childrenByParent[parentID]
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].CreatedAt != group[j].CreatedAt {
				return group[i].CreatedAt < group[j].CreatedAt
			}
			return group[i].ID < group[j].ID
		})
		childrenByParent[parentID] = group
	}

	result := make([]model.Comment, 0, len(comments))
	visited := make(map[int64]bool, len(comments))
	var appendTree func(model.Comment)
	appendTree = func(comment model.Comment) {
		if visited[comment.ID] {
			return
		}
		visited[comment.ID] = true
		result = append(result, comment)
		for _, child := range childrenByParent[comment.ID] {
			appendTree(child)
		}
	}
	for _, root := range roots {
		appendTree(root)
	}

	return result
}

// Like 点赞，并返回点赞后的评论（含「当前用户是否已赞」）。
// 先 GetByID 是为了在评论不存在或已软删时返回 ErrCommentNotFound；
// 重复点赞不报错（model 层幂等），因此这里不必先查是否已赞。
func (s *CommentService) Like(ctx context.Context, commentID int64, userID int64) (*model.Comment, error) {
	logging.Debugf("[comment] liking comment_id=%d user_id=%d", commentID, userID)
	if _, err := s.repo.GetByID(ctx, commentID); err != nil {
		logging.Errorf("[comment] like get comment failed id=%d: %v", commentID, err)
		return nil, err
	}

	if err := s.repo.AddLike(ctx, commentID, userID, time.Now().Unix()); err != nil {
		logging.Errorf("[comment] add like failed comment_id=%d user_id=%d: %v", commentID, userID, err)
		return nil, err
	}
	comment, err := s.repo.GetByIDForUser(ctx, commentID, int64Ptr(userID))
	if err != nil {
		logging.Errorf("[comment] reload liked comment failed id=%d: %v", commentID, err)
		return nil, err
	}
	logging.Infof("[comment] liked comment_id=%d user_id=%d", commentID, userID)
	return comment, nil
}

// Unlike 取消点赞并返回更新后的评论。同样先确认评论存在；
// 从未点过赞时静默成功，不视为错误。
func (s *CommentService) Unlike(ctx context.Context, commentID int64, userID int64) (*model.Comment, error) {
	logging.Debugf("[comment] unliking comment_id=%d user_id=%d", commentID, userID)
	if _, err := s.repo.GetByID(ctx, commentID); err != nil {
		logging.Errorf("[comment] unlike get comment failed id=%d: %v", commentID, err)
		return nil, err
	}

	if err := s.repo.RemoveLike(ctx, commentID, userID); err != nil {
		logging.Errorf("[comment] remove like failed comment_id=%d user_id=%d: %v", commentID, userID, err)
		return nil, err
	}
	comment, err := s.repo.GetByIDForUser(ctx, commentID, int64Ptr(userID))
	if err != nil {
		logging.Errorf("[comment] reload unliked comment failed id=%d: %v", commentID, err)
		return nil, err
	}
	logging.Infof("[comment] unliked comment_id=%d user_id=%d", commentID, userID)
	return comment, nil
}

func (s *CommentService) ListAll(ctx context.Context, p model.Pagination) ([]model.Comment, int64, error) {
	logging.Debugf("[comment] list all page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.ListAll(ctx, p)
}

func (s *CommentService) ListDeleted(ctx context.Context, p model.Pagination) ([]model.Comment, int64, error) {
	logging.Debugf("[comment] list deleted page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.ListDeleted(ctx, p)
}

// SoftDelete 软删一批评论。注意连带效应（详见 model 层说明）：
// 软删根评论会让它的子孙一并从前台消失，但那些子孙本身并未被标记删除。
func (s *CommentService) SoftDelete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	logging.Debugf("[comment] soft-deleting ids=%v", ids)
	if _, err := s.repo.SoftDelete(ctx, ids, time.Now().Unix()); err != nil {
		logging.Errorf("[comment] soft-delete failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[comment] soft-deleted ids=%v", ids)
	return nil
}

// Restore 恢复一条评论。只对当前处于删除态的评论生效，否则 ErrCommentNotFound。
func (s *CommentService) Restore(ctx context.Context, id int64) error {
	logging.Debugf("[comment] restoring id=%d", id)
	if err := s.repo.Restore(ctx, id, time.Now().Unix()); err != nil {
		logging.Errorf("[comment] restore failed id=%d: %v", id, err)
		return err
	}
	logging.Infof("[comment] restored id=%d", id)
	return nil
}

// BatchRestore 批量恢复评论，非删除态的 id 会被静默跳过。
func (s *CommentService) BatchRestore(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	logging.Debugf("[comment] batch-restoring ids=%v", ids)
	if _, err := s.repo.BatchRestore(ctx, ids, time.Now().Unix()); err != nil {
		logging.Errorf("[comment] batch-restore failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[comment] batch-restored ids=%v", ids)
	return nil
}

// HardDelete 永久删除回收站中的评论（活动态的删不掉），且不处理子评论。
func (s *CommentService) HardDelete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	logging.Debugf("[comment] permanently deleting ids=%v", ids)
	if _, err := s.repo.HardDelete(ctx, ids); err != nil {
		logging.Errorf("[comment] permanent delete failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[comment] permanently deleted ids=%v", ids)
	return nil
}

// normalizeNickname 与 viewmodel 层的同名逻辑保持一致，
// 让绕过 HTTP 层直接调用 service 的调用方也能拿到干净的显示名。
//
// 处理方式：换行/制表符折叠成空格，剔除其余控制字符，再按空白切分后以单个空格重组。
func normalizeNickname(value string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	return strings.Join(strings.Fields(cleaned), " ")
}

// validateCommentInput 校验评论的目标与内容。
//
// nicknameRequired 决定是否强制校验昵称：访客评论为 true，站长评论为 false。
// 长度用「字符数」（RuneCountInString）而不是字节数——否则一个中文昵称会因为
// UTF-8 占 3 字节而被误判超长。
func validateCommentInput(targetType string, targetID int64, content, nickname string, nicknameRequired bool) error {
	if _, ok := allowedCommentTargets[targetType]; !ok {
		return model.ErrInvalidCommentTarget
	}
	if targetID <= 0 {
		return model.ErrInvalidCommentTarget
	}
	if content == "" {
		return invalidf("content is required")
	}
	if utf8.RuneCountInString(content) > model.MaxCommentContentLength {
		return invalidf("content must be at most %d characters", model.MaxCommentContentLength)
	}
	if nicknameRequired {
		if nickname == "" {
			return invalidf("nickname is required")
		}
		if utf8.RuneCountInString(nickname) > model.MaxCommentNicknameLength {
			return invalidf("nickname must be at most %d characters", model.MaxCommentNicknameLength)
		}
	}
	return nil
}

// validateParent 校验回复目标：父评论必须存在，且与自身属于同一个目标
// （相同的 targetType 与 targetID）。parentID 为 nil 表示这是根评论，直接通过。
func (s *CommentService) validateParent(ctx context.Context, parentID *int64, targetType string, targetID int64) error {
	if parentID == nil {
		return nil
	}

	parent, err := s.repo.GetByID(ctx, *parentID)
	if err != nil {
		if errors.Is(err, model.ErrCommentNotFound) {
			return model.ErrCommentNotFound
		}
		return err
	}
	if parent.TargetType != targetType || parent.TargetID != targetID {
		return model.ErrInvalidCommentTarget
	}
	return nil
}

// int64Ptr 取地址，用于把 int64 放进「可空」字段。
func int64Ptr(v int64) *int64 {
	return &v
}
