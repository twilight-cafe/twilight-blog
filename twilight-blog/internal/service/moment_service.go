package service

import (
	"context"
	"errors"
	"time"

	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// MomentMediaStore 是动态服务对媒体的最小依赖，实现是 *MediaService。
//
// 与 ArticleMediaStore 的差别只有一处：**不提供 Delete**。动态的配图是
// 共享素材（一张图可以挂在多条动态、也可以是某篇文章的头图），永久删除一条动态
// 不该顺手销毁磁盘上的图片——那需要先证明「没有任何引用者了」，而动态这边
// 既没有那个查询、也不该有（见 model.MomentRepository 的说明）。
//
// 接口留窄是有意的：动态服务只需要问「这张图能不能用」。
type MomentMediaStore interface {
	Get(ctx context.Context, id int64) (*model.Media, error)
}

// MomentService 处理动态的业务规则。
//
// 与 ArticleService / PageService 的关键差别：动态不进全文搜索索引，
// 因此本服务不持有 search 依赖，写入后无需同步索引。
//
// 它持有 media 依赖，用来校验配图：这个字段在写入前必须确认「那张媒体存在
// 且确实是图片」。少了这一步会有两个后果，都是静默的：
//   - 引用一个不存在的 id 时，moment_media 上的外键会拒绝，而那个错误源于
//     客户端输入却被 handler 当成服务端故障报 500（见 validMomentMediaIDs）；
//   - 任何类型的媒体（一段视频、一个附件）都能被挂成动态配图，读的时候
//     viewmodel 会为它生成一个 <img src>，在读者面前变成一个破图。
//
// media 为 nil 表示该部署没有媒体服务（测试里传 nil）。此时跳过校验：
// 那种构造只出现在不涉及配图的测试里，而「宁可不校验也不 fail open」在这里
// 不成立——没有媒体服务就没有媒体可以在界面上引用，校验本身无从谈起。
type MomentService struct {
	repo  model.MomentRepository
	media MomentMediaStore
}

func NewMomentService(repo model.MomentRepository, media MomentMediaStore) *MomentService {
	return &MomentService{repo: repo, media: media}
}

// validMomentMediaIDs 校验一组配图 id，通过时返回去重后的副本。
//
// 为什么必须在这里做、而不是交给外键：外键能挡住「不存在」，但它报出来的是一句
// 驱动错误（"FOREIGN KEY constraint failed"），既不是 ValidationError、
// 也不带任何字段名，于是 handler 只能按 500 应答——而调用方给错了一个 id
// 是**客户端错误**，应当是 400。更糟的是这类错误以前连一条日志都没有。
//
// 三种失败都返回 *ValidationError（→ 400）：
//   - id 非正数；
//   - 媒体不存在（已删或从未存在）——媒体的删除是**硬删且没有回收站**，
//     所以「选图之后那张图被删了、再回来保存动态」是一条真实可达的路径；
//   - 媒体存在但不是图片。动态配图在前台是一组 <img>，指向视频只会得到一个破图。
//
// 去重是必要的：moment_media 的主键是 (moment_id, media_id)，重复 id 会被
// INSERT OR IGNORE 静默吞掉，但「调用方传了什么」与「实际存了什么」之间
// 就不一致了；这里先收敛一次，让返回值如实反映最终状态。
//
// 返回的是**新切片**而不是入参：调用方会把它存进结构体，共享底层数组意味着
// 此后任何一处改动都会被另一处看到。
func (s *MomentService) validMomentMediaIDs(ctx context.Context, mediaIDs []int64) ([]int64, error) {
	if len(mediaIDs) == 0 {
		return []int64{}, nil
	}
	if s.media == nil {
		return append([]int64{}, mediaIDs...), nil
	}

	seen := make(map[int64]struct{}, len(mediaIDs))
	valid := make([]int64, 0, len(mediaIDs))
	for _, id := range mediaIDs {
		if id <= 0 {
			return nil, invalidf("media_ids must contain only positive integers (got %d)", id)
		}
		if _, ok := seen[id]; ok {
			continue
		}

		media, err := s.media.Get(ctx, id)
		if err != nil {
			if errors.Is(err, model.ErrMediaNotFound) {
				return nil, invalidf("media %d does not exist", id)
			}
			return nil, err
		}
		if media.MediaType != model.MediaTypeImage {
			return nil, invalidf("media %d is %s, but a moment attachment must be an image", id, media.MediaType)
		}

		seen[id] = struct{}{}
		valid = append(valid, id)
	}
	return valid, nil
}

// Create 落库后重新读回一次再返回——这样返回值里的 Media 才会被装配好
// （写路径传入的只有 MediaIDs）。
//
// 配图先过 validMomentMediaIDs：不合法的 id 在这里就变成 *ValidationError（→ 400），
// 而不是让外键在事务里抛一句驱动错误、被 handler 当成 500。
func (s *MomentService) Create(ctx context.Context, content string, mediaIDs []int64) (*model.Moment, error) {
	logging.Debugf("[moment] creating moment content_len=%d media_count=%d", len(content), len(mediaIDs))

	valid, err := s.validMomentMediaIDs(ctx, mediaIDs)
	if err != nil {
		logging.Warnf("[moment] refused to create with invalid media_ids=%v: %v", mediaIDs, err)
		return nil, err
	}

	now := time.Now().Unix()
	moment := &model.Moment{
		Content:   content,
		MediaIDs:  valid,
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
//
// 校验排在写入之前：图不存在或不是图片时直接返回，库里那条动态一个字段都
// 不会被动到（此刻所有改动都还只在内存里）。
func (s *MomentService) Update(ctx context.Context, id int64, content string, mediaIDs *[]int64) (*model.Moment, error) {
	logging.Debugf("[moment] updating id=%d content_len=%d media_provided=%t", id, len(content), mediaIDs != nil)
	moment, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logging.Errorf("[moment] get for update failed id=%d: %v", id, err)
		return nil, err
	}

	moment.Content = content
	if mediaIDs != nil {
		valid, err := s.validMomentMediaIDs(ctx, *mediaIDs)
		if err != nil {
			logging.Warnf("[moment] refused to update id=%d with invalid media_ids=%v: %v", id, *mediaIDs, err)
			return nil, err
		}
		moment.MediaIDs = valid
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
