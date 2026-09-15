package viewmodel

// InstallRequest 是首次安装时设置管理员密码的请求体。
type InstallRequest struct {
	Password string `json:"password"`
}

// InstallStatusResponse 告知前端是否已完成安装，
// 用于决定进入安装向导还是登录页。
type InstallStatusResponse struct {
	Installed bool `json:"installed"`
}
