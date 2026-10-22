package service

import (
	"context"
	"strings"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// TagService 处理标签的业务规则。
// 本层很薄：标签没有独立的创建入口，因此这里只做输入的空白裁剪与「改名后回读」。
type TagService struct {
	repo model.TagRepository
}

func NewTagService(repo model.TagRepository) *TagService {
	return &TagService{repo: repo}
}

func (s *TagService) List(ctx context.Context, p model.Pagination) ([]model.Tag, int64, error) {
	logging.Debugf("[tag] list page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.List(ctx, p)
}

func (s *TagService) ListArticles(ctx context.Context, tagID int64, p model.Pagination) ([]model.Article, int64, error) {
	logging.Debugf("[tag] list articles tag_id=%d page=%d page_size=%d", tagID, p.Page, p.PageSize)
	return s.repo.ListArticlesByTag(ctx, tagID, p)
}

func (s *TagService) Count(ctx context.Context, tagID int64) (int64, error) {
	logging.Debugf("[tag] count tag_id=%d", tagID)
	return s.repo.CountByTag(ctx, tagID)
}

// Rename 校验并修改标签的中英文名，然后回读返回最新对象。
//
// 中文名只做 TrimSpace，空名返回 ErrInvalidTagName；英文名同样 TrimSpace，
// 但**允许为空**（表示不提供译名，前台回退中文）。英文名不参与唯一约束，
// 所以这里不做重名检查——重名检查只针对中文名，在 model 层完成。
//
// 两个名字在同一次调用里提交：分两次改名会出现「中文已改、英文还是旧的」
// 这种中间状态，而界面上只有一个保存按钮。
func (s *TagService) Rename(ctx context.Context, tagID int64, name, nameEN string) (*model.Tag, error) {
	name = strings.TrimSpace(name)
	nameEN = strings.TrimSpace(nameEN)
	if name == "" {
		return nil, model.ErrInvalidTagName
	}

	logging.Debugf("[tag] renaming id=%d name=%q name_en=%q", tagID, name, nameEN)
	if err := s.repo.Rename(ctx, tagID, name, nameEN); err != nil {
		logging.Errorf("[tag] rename failed id=%d name=%q: %v", tagID, name, err)
		return nil, err
	}
	tag, err := s.repo.GetByID(ctx, tagID)
	if err != nil {
		logging.Errorf("[tag] get after rename failed id=%d: %v", tagID, err)
		return nil, err
	}
	logging.Infof("[tag] renamed id=%d name=%q", tagID, name)
	return tag, nil
}
