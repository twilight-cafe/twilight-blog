package service

import (
	"context"
	"time"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// PageSearchIndexer 把外部 Meilisearch 索引与页面库的变更保持同步。
// 与 ArticleSearchIndexer 一样，删除有单条与批量两个入口：批量必须走
// DeletePages，否则成本会与请求里的 id 个数成正比（实测约 104ms/id）。
type PageSearchIndexer interface {
	IndexPage(context.Context, *model.Page) error
	DeletePage(context.Context, int64) error
	DeletePages(context.Context, []int64) error
}

// PageService 处理独立页面的业务规则，结构与 ArticleService 平行——
// 同样在库写入后尽力同步搜索索引。差别是页面没有标签，因此没有归一化步骤。
type PageService struct {
	repo   model.PageRepository
	search PageSearchIndexer
}

func NewPageService(repo model.PageRepository, search PageSearchIndexer) *PageService {
	return &PageService{repo: repo, search: search}
}

// indexPage 尽力把页面推送进搜索索引；失败只记日志，不影响库写入的结果。
func (s *PageService) indexPage(ctx context.Context, page *model.Page) {
	if s.search == nil {
		return
	}
	if err := s.search.IndexPage(ctx, page); err != nil {
		logging.Errorf("[page] search index update failed id=%d: %v", page.ID, err)
	}
}

// deletePages 一次从索引里移除多个页面。理由同 ArticleService.deleteArticles：
// 逐条循环会让成本与 id 个数成正比（每次同步等一个 Meilisearch 任务）。
// 同样地，被它取代的单条包装已删掉（见 ArticleService 同处的说明）。
func (s *PageService) deletePages(ctx context.Context, ids []int64) {
	if s.search == nil || len(ids) == 0 {
		return
	}
	if err := s.search.DeletePages(ctx, ids); err != nil {
		logging.Errorf("[page] search batch delete failed count=%d: %v", len(ids), err)
	}
}

func (s *PageService) Create(ctx context.Context, title, titleEN, content, contentEN string) (*model.Page, error) {
	logging.Debugf("[page] creating page title=%q content_len=%d", title, len(content))
	now := time.Now().Unix()
	page := &model.Page{
		Title:     title,
		TitleEN:   titleEN,
		Content:   content,
		ContentEN: contentEN,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.Create(ctx, page); err != nil {
		logging.Errorf("[page] create failed title=%q: %v", title, err)
		return nil, err
	}
	logging.Infof("[page] created id=%d title=%q", page.ID, page.Title)
	s.indexPage(ctx, page)
	return page, nil
}

// Update 覆盖标题与正文。先读后写，同样存在读-改-写竞态。
//
// content / contentEN / titleEN 为 nil 表示「这次不改这一份」——与文章 Update
// 同一套语义，理由也同一个：非指针时只改中文标题的 PUT 会静默清空英文版本。
func (s *PageService) Update(ctx context.Context, id int64, title string, titleEN *string, content, contentEN *string) (*model.Page, error) {
	logging.Debugf("[page] updating id=%d title=%q content_provided=%t", id, title, content != nil)
	page, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logging.Errorf("[page] get for update failed id=%d: %v", id, err)
		return nil, err
	}

	page.Title = title
	if titleEN != nil {
		page.TitleEN = *titleEN
	}
	if content != nil {
		page.Content = *content
	}
	if contentEN != nil {
		page.ContentEN = *contentEN
	}
	page.UpdatedAt = time.Now().Unix()

	if err := s.repo.Update(ctx, page); err != nil {
		logging.Errorf("[page] update failed id=%d: %v", id, err)
		return nil, err
	}
	logging.Infof("[page] updated id=%d title=%q", page.ID, page.Title)
	s.indexPage(ctx, page)
	return page, nil
}

func (s *PageService) SoftDelete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	logging.Debugf("[page] soft-deleting ids=%v", ids)
	if _, err := s.repo.SoftDelete(ctx, ids, time.Now().Unix()); err != nil {
		logging.Errorf("[page] soft-delete failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[page] soft-deleted ids=%v", ids)
	s.deletePages(ctx, ids)
	return nil
}

func (s *PageService) Restore(ctx context.Context, id int64) error {
	logging.Debugf("[page] restoring id=%d", id)
	if err := s.repo.Restore(ctx, id, time.Now().Unix()); err != nil {
		logging.Errorf("[page] restore failed id=%d: %v", id, err)
		return err
	}
	logging.Infof("[page] restored id=%d", id)
	if restored, err := s.repo.GetByID(ctx, id); err == nil {
		s.indexPage(ctx, restored)
	} else {
		logging.Errorf("[page] reindex after restore failed id=%d: %v", id, err)
	}
	return nil
}

func (s *PageService) BatchRestore(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	logging.Debugf("[page] batch-restoring ids=%v", ids)
	if _, err := s.repo.BatchRestore(ctx, ids, time.Now().Unix()); err != nil {
		logging.Errorf("[page] batch-restore failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[page] batch-restored ids=%v", ids)
	for _, id := range ids {
		if restored, err := s.repo.GetByID(ctx, id); err == nil {
			s.indexPage(ctx, restored)
		} else {
			logging.Errorf("[page] reindex after batch restore failed id=%d: %v", id, err)
		}
	}
	return nil
}

func (s *PageService) HardDelete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	logging.Debugf("[page] permanently deleting ids=%v", ids)
	if _, err := s.repo.HardDelete(ctx, ids); err != nil {
		logging.Errorf("[page] permanent delete failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[page] permanently deleted ids=%v", ids)
	s.deletePages(ctx, ids)
	return nil
}

func (s *PageService) Get(ctx context.Context, id int64) (*model.Page, error) {
	logging.Debugf("[page] get id=%d", id)
	return s.repo.GetByID(ctx, id)
}

func (s *PageService) List(ctx context.Context, p model.Pagination) ([]model.Page, int64, error) {
	logging.Debugf("[page] list page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.List(ctx, p)
}

func (s *PageService) ListDeleted(ctx context.Context, p model.Pagination) ([]model.Page, int64, error) {
	logging.Debugf("[page] list deleted page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.ListDeleted(ctx, p)
}
