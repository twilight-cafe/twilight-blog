package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/config"
	"blog-engine/internal/logging"
	"blog-engine/internal/model"
	"blog-engine/internal/service"
	"blog-engine/internal/viewmodel"
)

// 首次安装端点。
//
// 安装是**一次性**动作，而且它的一次性不再由 settings.json 决定，而是由数据库
// 决定：users 表里 is_admin = 1 的那一行存在，就算装好了。
// 它不在任何鉴权分组下——这正是它必须自带密码强度校验与并发保护的缘故：
// 这是全站唯一一个「未登录即可创建管理员」的入口。
//
// 并发保护也不再需要互斥锁：单管理员唯一索引（idx_users_single_admin）在数据库
// 层钉死了「最多一个管理员」，两个并发请求里必然有一个撞上约束。锁能挡住的只有
// 同一进程内的两个 goroutine，而索引连多进程、多实例都挡得住，所以这里用一个
// 更强也更简单的保证替换掉了原来那个 sync.Mutex。
type InstallHandler struct {
	auth   service.AdminCredentialsStore
	config *config.Config
}

func NewInstallHandler(auth service.AdminCredentialsStore, cfg *config.Config) *InstallHandler {
	return &InstallHandler{auth: auth, config: cfg}
}

// Status 对应 GET /install_status，报告博客是否已安装，
// 供前端决定跳安装向导还是登录页。
// 无需鉴权：返回值只有一个布尔，泄露的信息量可以忽略。
func (h *InstallHandler) Status(c echo.Context) error {
	logging.Debugf("[handler:install] Status called method=%s path=%s", c.Request().Method, c.Request().URL.Path)

	installed, err := h.auth.Exists(c.Request().Context())
	if err != nil {
		logging.Errorf("[install] check installation state failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read installation state")
	}
	return c.JSON(http.StatusOK, viewmodel.InstallStatusResponse{Installed: installed})
}

// Install 对应 POST /install，创建管理员行（is_admin = 1）完成安装。
//
// 顺序上两个要点：
//  1. 密码强度在**写库之前**校验。服务端才是密码策略的权威——安装表单的
//     minlength 可以被绕过（直接打这个接口即可），所以不能依赖前端校验。
//  2. 插入之前不做任何写入，所以**不再需要**旧版那段「写盘失败就把内存里的哈希
//     回滚成原值」的补偿逻辑：唯一的写操作是那条 INSERT，而它要么成功、要么
//     什么都没变。settings.json 也不再是安全关键状态——真正的安装标记在库里。
//
// 请求体形状保持不变（只有 password 一个键）：管理员登录名取默认值 admin，
// 安装之后可以在控制台里改。为此保留一个键是**有意**的取舍，见
// twilight-blog-api/api.md 的安装一节。
func (h *InstallHandler) Install(c echo.Context) error {
	logging.Debugf("[handler:install] Install called method=%s path=%s", c.Request().Method, c.Request().URL.Path)

	var req viewmodel.InstallRequest
	if err := c.Bind(&req); err != nil {
		logging.Warnf("[install] invalid install request: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request")
	}

	req.Password = strings.TrimSpace(req.Password)
	if err := config.ValidatePasswordStrength(req.Password); err != nil {
		logging.Warnf("[install] rejected password remote=%s: %v", c.RealIP(), err)
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()

	// 已经装好时明确拒绝。这不是并发保护（那由唯一索引负责），而是为了让
	// 「重复安装」给出一个能读懂的回答，而不是让操作员对着一个 500 猜。
	installed, err := h.auth.Exists(ctx)
	if err != nil {
		logging.Errorf("[install] check installation state failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read installation state")
	}
	if installed {
		logging.Warnf("[install] installation attempted when already installed remote=%s", c.RealIP())
		return echo.NewHTTPError(http.StatusConflict, "already installed")
	}

	// 登录名取默认值 "admin"：安装请求体里没有这个字段，而新增一个键会改变
	// 外部 API 形状（不变量 I2）。装好之后可以在控制台里改。
	username := config.DefaultAdminName

	// 空串哨兵：users.email 是 NOT NULL UNIQUE，而 '' 是任何注册路径都无法
	// 产生的地址（normalizeAndValidateEmail 明确拒绝空值），因此管理员天然
	// 不可注册，不需要任何额外的守卫代码。
	if _, err := h.auth.CreateAdmin(ctx, username, username, req.Password); err != nil {
		if errors.Is(err, model.ErrAdminAlreadyExists) {
			// 另一个并发请求抢先装好了。对它来说「已安装」就是正确的答案。
			logging.Warnf("[install] a concurrent request installed the blog first remote=%s", c.RealIP())
			return echo.NewHTTPError(http.StatusConflict, "already installed")
		}
		// 强度校验在函数入口已经做过一次；service 里再做一次是刻意的重复，
		// 好让「密码策略只有一份实现」这件事不依赖任何调用方记得去校验。
		// 真走到这个分支说明两次判断不一致，按 400 回答而不是假装成功。
		var validationErr *service.ValidationError
		if errors.As(err, &validationErr) {
			return echo.NewHTTPError(http.StatusBadRequest, validationErr.Message)
		}
		logging.Errorf("[install] create administrator failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save the administrator account")
	}

	logging.Infof("[install] blog engine installed remote=%s", c.RealIP())
	return c.JSON(http.StatusOK, viewmodel.InstallStatusResponse{Installed: true})
}
