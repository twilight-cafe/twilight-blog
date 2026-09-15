package service

import (
	"context"
	"time"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// MomentService 处理动态的业务规则。
//
// 与 ArticleService / PageService 的关键差别：动态不进全文搜索索引，
// 因此本服务不持有 search 依赖，写入后无需同步索引。
type MomentService struct {
	repo model.MomentRepository
}

func NewMomentService(repo model.MomentRepository) *MomentService {
	return &MomentService{repo: repo}
}

// Create 落库后重新读回一次再返回——这样返回值里的 Media 才会被装配好
// （写路径传入的只有 MediaIDs）。
func (s *MomentService) Create(ctx context.Context, content string, mediaIDs []int64) (*model.Moment, error) {
	logging.Debugf("[moment] creating moment content_len=%d media_count=%d", len(content), len(mediaIDs))
	now := time.Now().Unix()
	moment := &model.Moment{
		Content:   content,
		MediaIDs:  mediaIDs,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.Create(ctx, moment); err != nil {
		logging.Errorf("[moment] create failed: %v", err)
		return nil, err
	}
	logging.Infof("[moment] created id=%d content_len=%d", moment.ID, len(moment.Content))
	return s.repo.GetByID(ctx, moment.ID)
}

// Update 用给定字段覆盖动态。mediaIDs 为 nil 表示调用方没有传这个字段，
// 此时保留原有配图；传空切片则表示「清空配图」——两者语义不同。
// 同样是先读后写，存在读-改-写竞态。
func (s *MomentService) Update(ctx context.Context, id int64, content string, mediaIDs *[]int64) (*model.Moment, error) {
	logging.Debugf("[moment] updating id=%d content_len=%d media_provided=%t", id, len(content), mediaIDs != nil)
	moment, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logging.Errorf("[moment] get for update failed id=%d: %v", id, err)
		return nil, err
	}

	moment.Content = content
	if mediaIDs != nil {
		moment.MediaIDs = *mediaIDs
	}
	moment.UpdatedAt = time.Now().Unix()

	if err := s.repo.Update(ctx, moment); err != nil {
		logging.Errorf("[moment] update failed id=%d: %v", id, err)
		return nil, err
	}
	logging.Infof("[moment] updated id=%d content_len=%d", moment.ID, len(moment.Content))
	return s.repo.GetByID(ctx, id)
}

func (s *MomentService) SoftDelete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	logging.Debugf("[moment] soft-deleting ids=%v", ids)
	if _, err := s.repo.SoftDelete(ctx, ids, time.Now().Unix()); err != nil {
		logging.Errorf("[moment] soft-delete failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[moment] soft-deleted ids=%v", ids)
	return nil
}

func (s *MomentService) Restore(ctx context.Context, id int64) error {
	logging.Debugf("[moment] restoring id=%d", id)
	if err := s.repo.Restore(ctx, id, time.Now().Unix()); err != nil {
		logging.Errorf("[moment] restore failed id=%d: %v", id, err)
		return err
	}
	logging.Infof("[moment] restored id=%d", id)
	return nil
}

func (s *MomentService) BatchRestore(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	logging.Debugf("[moment] batch-restoring ids=%v", ids)
	if _, err := s.repo.BatchRestore(ctx, ids, time.Now().Unix()); err != nil {
		logging.Errorf("[moment] batch-restore failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[moment] batch-restored ids=%v", ids)
	return nil
}

func (s *MomentService) HardDelete(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	logging.Debugf("[moment] permanently deleting ids=%v", ids)
	if _, err := s.repo.HardDelete(ctx, ids); err != nil {
		logging.Errorf("[moment] permanent delete failed ids=%v: %v", ids, err)
		return err
	}
	logging.Infof("[moment] permanently deleted ids=%v", ids)
	return nil
}

func (s *MomentService) Get(ctx context.Context, id int64) (*model.Moment, error) {
	logging.Debugf("[moment] get id=%d", id)
	return s.repo.GetByID(ctx, id)
}

func (s *MomentService) List(ctx context.Context, p model.Pagination) ([]model.Moment, int64, error) {
	logging.Debugf("[moment] list page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.List(ctx, p)
}

func (s *MomentService) ListDeleted(ctx context.Context, p model.Pagination) ([]model.Moment, int64, error) {
	logging.Debugf("[moment] list deleted page=%d page_size=%d", p.Page, p.PageSize)
	return s.repo.ListDeleted(ctx, p)
}
