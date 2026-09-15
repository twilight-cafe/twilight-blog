package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/logging"
	"blog-engine/internal/viewmodel"
)

// SiteHandler 负责站点级文案设置（目前只有「网站描述」）。
//
// 与 AuthHandler 里的站长资料分开，是因为这两件事的受众不同：
// 读接口是**公开**的（描述本来就展示给所有读者），写接口仅管理员可用。
// 把它挂在 AuthHandler 上会让「读公开、写受限」这个分野淹没在一堆鉴权方法里。
//
// 落盘方式沿用 config.Save：settings.json 总是整份写出，
// 所以「先改内存、再落盘、失败回滚」是这里的标准三步。
type SiteHandler struct {
	config       *config.Config
	settingsPath string
}

func NewSiteHandler(cfg *config.Config, settingsPath string) *SiteHandler {
	return &SiteHandler{config: cfg, settingsPath: settingsPath}
}

// Get 对应 GET /api/site（公开）。
//
// 无需鉴权：返回的就是前台首页与页脚已经在展示的那段文案，
// 任何访问者本来就能读到。它存在的意义是让 SPA 把这些文案集中取一次，
// 而不是在每个组件里各写一份硬编码。
func (h *SiteHandler) Get(c echo.Context) error {
	logging.Debugf("[handler:site] Get called path=%s", c.Request().URL.Path)
	return c.JSON(http.StatusOK, viewmodel.SiteSettingsResponse{
		Description: h.config.SiteDescriptionValue(),
	})
}

// Update 对应 PUT /api/admin/site（仅管理员，见 router 的 admin 分组）。
//
// 写盘失败时把内存回滚成旧值：否则进程内读到的是新描述、磁盘上还是旧的，
// 重启之后「刚才明明保存成功了」会变成一句无法复现的抱怨。
// 这与 install handler 处理密码哈希的方式一致。
func (h *SiteHandler) Update(c echo.Context) error {
	logging.Debugf("[handler:site] Update called path=%s", c.Request().URL.Path)

	var req viewmodel.SiteSettingsRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}
	if err := req.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	previous := h.config.SiteDescriptionValue()
	h.config.SetSiteDescription(req.Description)

	if err := h.config.Save(h.settingsPath); err != nil {
		h.config.SetSiteDescription(previous)
		logging.Errorf("[site] persist settings failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save site settings")
	}

	logging.Infof("[site] site description updated")
	return c.JSON(http.StatusOK, viewmodel.SiteSettingsResponse{
		Description: h.config.SiteDescriptionValue(),
	})
}
