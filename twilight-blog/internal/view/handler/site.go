package handler

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/logging"
	"blog-engine/internal/viewmodel"
)

// AdminAvatarSource 是站点公开信息对「站长头像」的**最小**依赖。
//
// 只有一个只读方法：SiteHandler 负责的是「对外展示的站点信息」，
// 不该因为管理员凭据那边需要改密码、改用户名而依赖一整个凭据服务。
// 实现是 *service.AdminAuth。
type AdminAvatarSource interface {
	AvatarMediaID(ctx context.Context) (int64, bool)
}

// SiteHandler 负责站点级对外信息（网站描述与站长头像）。
//
// 与 AuthHandler 里的站长资料分开，是因为这两件事的受众不同：
// 读接口是**公开**的（描述与头像本来就展示给所有读者），写接口仅管理员可用。
// 把它挂在 AuthHandler 上会让「读公开、写受限」这个分野淹没在一堆鉴权方法里。
//
// 描述落盘方式沿用 config.Save：settings.json 总是整份写出，
// 所以「先改内存、再落盘、失败回滚」是这里的标准三步。
// 头像不属于 config，它住在 users.avatar_media_id 上，由 PUT /api/admin/profile 写
// （与展示名、登录名同一条路径），本 handler 只负责读出来一起下发。
type SiteHandler struct {
	config       *config.Config
	settingsPath string
	avatars      AdminAvatarSource
}

func NewSiteHandler(cfg *config.Config, settingsPath string, avatars AdminAvatarSource) *SiteHandler {
	return &SiteHandler{config: cfg, settingsPath: settingsPath, avatars: avatars}
}

// Get 对应 GET /api/site（公开）。
//
// 无需鉴权：返回的就是前台首页、页脚与侧栏已经在展示的那些内容，
// 任何访问者本来就能读到。它存在的意义是让 SPA 把这些内容集中取一次，
// 而不是在每个组件里各写一份硬编码。
func (h *SiteHandler) Get(c echo.Context) error {
	logging.Debugf("[handler:site] Get called path=%s", c.Request().URL.Path)

	// 头像读不到（未安装、或那一列是 NULL）时给空串而不是省略字段：
	// 前端需要能区分「没有头像」（回退占位块）与「这个字段不存在」。
	return c.JSON(http.StatusOK, viewmodel.SiteSettingsResponse{
		Description:       h.config.SiteDescriptionValue(),
		AvatarURL:         h.currentAvatarURL(c),
		CodeBlockMaxLines: h.config.CodeBlockMaxLinesValue(),
		HomePageSize:      h.config.HomePageSizeValue(),
		BorderRadius:      h.config.BorderRadiusValue(),
	})
}

// Update 对应 PUT /api/admin/site（仅管理员，见 router 的 admin 分组）。
//
// 写盘失败时把内存回滚成旧值：否则进程内读到的是新描述、磁盘上还是旧的，
// 重启之后「刚才明明保存成功了」会变成一句无法复现的抱怨。
// 这与 install handler 处理密码哈希的方式一致。
//
// 注意这个端点只写「站点文案级」的设置（描述、代码块行数）。站长头像虽然在
// 同一条公开读路径上下发，但它属于账号资料（users.avatar_media_id），
// 写入在 PUT /api/admin/profile 上——让「站点文案」与「账号资料」各自
// 只有一个写入口，读者不必猜哪个才是权威。
func (h *SiteHandler) Update(c echo.Context) error {
	logging.Debugf("[handler:site] Update called path=%s", c.Request().URL.Path)

	var req viewmodel.SiteSettingsRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	previousDescription := h.config.SiteDescriptionValue()
	previousLines := h.config.CodeBlockMaxLinesValue()
	previousPageSize := h.config.HomePageSizeValue()
	previousRadius := h.config.BorderRadiusValue()
	h.config.SetSiteDescription(req.Description)
	// 行数是三态（nil=保持现值），所以这里不是无条件调用。
	// 先暂存合法化后的值：SetCodeBlockMaxLines 可能存的是请求里的指针值，
	// 回滚时必须写回**进入 handler 时的值**而不是指针指向的内容。
	newLines := previousLines
	if req.CodeBlockMaxLines != nil {
		if err := h.config.SetCodeBlockMaxLines(*req.CodeBlockMaxLines); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		newLines = *req.CodeBlockMaxLines
	}
	newPageSize := previousPageSize
	if req.HomePageSize != nil {
		if err := h.config.SetHomePageSize(*req.HomePageSize); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		newPageSize = *req.HomePageSize
	}
	// 圆角同样是三态。注意回滚比较用的是**进入 handler 时的值**：
	// 若写成 `if newRadius != *req.BorderRadius`，请求里恰好等于旧值时就不会回滚，
	// 但那种情况下本来也没改动，无害；用 previousRadius 比较更直观且不会漏。
	newRadius := previousRadius
	if req.BorderRadius != nil {
		if err := h.config.SetBorderRadius(*req.BorderRadius); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		newRadius = *req.BorderRadius
	}

	if err := h.config.Save(h.settingsPath); err != nil {
		h.config.SetSiteDescription(previousDescription)
		if newLines != previousLines {
			_ = h.config.SetCodeBlockMaxLines(previousLines)
		}
		if newPageSize != previousPageSize {
			_ = h.config.SetHomePageSize(previousPageSize)
		}
		if newRadius != previousRadius {
			_ = h.config.SetBorderRadius(previousRadius)
		}
		logging.Errorf("[site] persist settings failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save site settings")
	}

	logging.Infof("[site] site settings updated")
	return c.JSON(http.StatusOK, viewmodel.SiteSettingsResponse{
		Description: h.config.SiteDescriptionValue(),
		// 与 Get 保持同一形状：写描述不会动头像，但响应若少一个字段，
		// 前端「拿响应替换本地缓存」的写法就会把头像抹掉。
		AvatarURL:         h.currentAvatarURL(c),
		CodeBlockMaxLines: newLines,
		HomePageSize:      newPageSize,
		BorderRadius:      newRadius,
	})
}

// currentAvatarURL 是 Get 与 Update 共用的头像读取，避免两处响应漂移。
func (h *SiteHandler) currentAvatarURL(c echo.Context) string {
	if h.avatars == nil {
		return ""
	}
	id, ok := h.avatars.AvatarMediaID(c.Request().Context())
	if !ok {
		return ""
	}
	return viewmodel.MediaURL(id)
}
