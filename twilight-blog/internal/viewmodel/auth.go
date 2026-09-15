package viewmodel

import (
	"fmt"
	"strings"

	"blog-engine/internal/model"
	"blog-engine/internal/session"

	"blog-engine/internal/logging"
)

// 本包在登录相关结构上遵守一条规则：**用户名与昵称是两个字段，永不合并**。
// 用户名（username）是登录标识，昵称（nickname）是前台展示名。
// 旧版把两者混为一谈（管理员用 admin_name 同时充当二者、访客只有 nickname），
// 结果是「改个显示名」会连带改掉登录方式。新增字段时请沿用这个分法。

// LoginRequest 是**全站唯一**的登录请求：用户名 + 密码。
//
// 登录界面不再先让用户声明「我是管理员」还是「我是普通用户」——那个选择既多余
// 又容易选错（选错了得到的错误提示还一模一样）。身份由**用户名**决定：这个名字
// 落在站长的登录名上就是站长，否则去 users 表里找账号。
//
// 这条规则能成立的前提是两者共用同一个命名空间：站长的登录名与访客的用户名
// 全局唯一，任何写入路径都不允许撞上站长的名字（见 service.UserService 里的
// 预留判定）。否则一个与站长同名的访客账号将永远登不进来——它的名字会被判给站长。
type LoginRequest struct {
	Username string `json:"username"`
	// Email 是**兼容字段**：username 这一列是后加的，老访客不一定知道自己的
	// 用户名是什么（由 backfillUsernames 从邮箱推出来的），用当初注册的邮箱
	// 登进来是最不意外的路径。新代码一律只填 Username。
	//
	// 它没有把「这是哪个身份」也一并暴露出去：站长的登录名必须通过用户名的
	// 形状校验，不含 '@'，所以邮箱永远落在访客那一支。
	//
	// Deprecated: 请改用 Username。
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Identifier 返回真正要拿去比对登录标识的那个串。
//
// 两个字段都填时以 Username 为准，避免「哪个优先」变成一个需要读实现才知道的约定。
func (r *LoginRequest) Identifier() string {
	if strings.TrimSpace(r.Username) != "" {
		return strings.TrimSpace(r.Username)
	}
	return strings.TrimSpace(r.Email)
}

// EmailCodeRequest 请求发送一枚验证码。
// 注册与重置密码共用同一枚码，因为两种情况下收件人都必须已经控制该邮箱。
type EmailCodeRequest struct {
	Email string `json:"email"`
}

// RegisterRequest 用一枚验证码完成注册。
//
// Email 与 Username 是两件事，不要互换：
//   - Email 是收验证码的地方，用来证明「确实是本人」；
//   - Username 是今后登录时要填的东西，全站唯一。
type RegisterRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Password string `json:"password"`
}

// ResetPasswordRequest 用一枚验证码设置新密码。
type ResetPasswordRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

// ChangePasswordRequest 供已登录账号修改密码，必须同时提供当前密码。
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// UpdateProfileRequest 是访客可自助修改的两项资料。
//
// 用指针而不是字符串，是为了区分「没传这个字段」与「传了空串」：
// 前者表示「这一项不改」，后者应当被拒绝（用户名与昵称都不能为空）。
// 两者用同一个值表示的话，只想改昵称的请求会顺手把用户名清空。
//
// 邮箱不在其中——它是已验证的联系方式，只有管理员可以改派。
type UpdateProfileRequest struct {
	Username *string `json:"username"`
	Nickname *string `json:"nickname"`
}

// AdminProfileRequest 修改站长的资料，两个字段分别对应登录与展示。
type AdminProfileRequest struct {
	// Name 是**展示名**：后台侧栏、页脚与评论署名用它。不传则保持不变。
	Name string `json:"name"`
	// Username 是**登录名**。指针为 nil 表示不改动——改登录名是一件会影响
	// 下次登录方式的事，不该由「碰巧没传这个键」来触发。
	Username *string `json:"username"`
	// AvatarMediaID 是站长头像那张媒体的 id，**三态**：字段缺失 = 不动，
	// 显式 null = 摘掉头像，数字 = 换成这张。与文章的 CoverMediaID 同一条规则，
	// 理由也一样——*int64 表达不了三态，而塌缩总会朝某个方向损坏数据
	// （要么用户摘不掉头像，要么一次只改显示名的保存静默删掉头像）。
	//
	// 它与 articles.cover_media_id 同为**引用**：换图/摘除都不删媒体文件。
	AvatarMediaID OptionalID `json:"avatar_media_id"`
}

// SetUsername 报告请求里是否出现了 username 字段。
// 放在这里而不是让 handler 直接判 nil，是为了让「哪些字段是可选且三态/两态的」
// 这件事留在请求类型自己身上，handler 只表达意图。
func (r *AdminProfileRequest) SetUsername() bool { return r.Username != nil }

// SetAvatarMediaID 报告请求里是否出现了 avatar_media_id 字段（哪怕是 null）。
func (r *AdminProfileRequest) SetAvatarMediaID() bool { return r.AvatarMediaID.Set }

// AdminUpdateUserRequest 供管理员订正他人账号的三项资料。
// username 是登录标识，nickname 是展示名，email 是联系方式；
// 三个字段各自受自己的唯一约束约束（email 与 username 都唯一）。
type AdminUpdateUserRequest struct {
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
}

// UserResponse 是账号视图。之所以可以直接包含邮箱，
// 是因为只有账号本人与管理员会收到这个形状。
type UserResponse struct {
	ID int64 `json:"id"`
	// Username 是登录标识；前端只在「我的资料」与管理员用户列表里显示。
	Username string `json:"username"`
	// Nickname 是公开展示名；评论、列表等所有面向读者的地方都用它。
	Nickname string `json:"nickname"`
	// Email 是联系方式，不参与登录。
	Email       string `json:"email"`
	HasPassword bool   `json:"has_password"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// MeResponse 描述当前会话，供前端决定渲染哪个后台界面。
type MeResponse struct {
	// Role 取 "admin" 或 "user"（见 session.RoleAdmin / RoleUser）。
	Role string `json:"role"`
	// Name 是站长的**展示名**；访客账号下为空。
	Name string `json:"name,omitempty"`
	// Username 是站长的**登录名**；只有 role=admin 时出现。
	//
	// 单独给出它，是为了让「账号」页能把「登录用哪个名字」和「前台显示什么」
	// 分成两个输入框——只有一个名字字段时，界面只能猜，而猜错的代价是
	// 用户以为自己改了昵称、实际上改掉了登录方式。
	Username string `json:"username,omitempty"`
	// User 是访客账号；站长没有 users 表记录，因此该字段缺席。
	User *UserResponse `json:"user,omitempty"`
}

func NewUserResponse(user *model.User) UserResponse {
	logging.Debugf("[viewmodel] NewUserResponse called")
	return UserResponse{
		ID:          user.ID,
		Username:    user.Username,
		Nickname:    user.Nickname,
		Email:       user.Email,
		HasPassword: user.HasPassword(),
		CreatedAt:   user.CreatedAt,
		UpdatedAt:   user.UpdatedAt,
	}
}

func NewUserResponseList(users []model.User) []UserResponse {
	result := make([]UserResponse, 0, len(users))
	for i := range users {
		result = append(result, NewUserResponse(&users[i]))
	}
	return result
}

// NewAdminMeResponse 描述站长的会话：角色、展示名与登录名，没有 User 对象。
// username 与 name 分别是登录标识与展示名，调用方不要把同一个值传两次。
func NewAdminMeResponse(name, username string) MeResponse {
	return MeResponse{Role: string(session.RoleAdmin), Name: name, Username: username}
}

// NewUserMeResponse 描述访客的会话：角色为 user，并附带账号资料。
func NewUserMeResponse(user *model.User) MeResponse {
	response := NewUserResponse(user)
	return MeResponse{Role: string(session.RoleUser), User: &response}
}

func (r *LoginRequest) Validate() error {
	r.Username = strings.TrimSpace(r.Username)
	r.Email = strings.TrimSpace(r.Email)
	r.Password = strings.TrimSpace(r.Password)
	if r.Identifier() == "" || r.Password == "" {
		return fmt.Errorf("username and password are required")
	}
	return nil
}

func (r *EmailCodeRequest) Validate() error {
	r.Email = strings.TrimSpace(r.Email)
	if r.Email == "" {
		return fmt.Errorf("email is required")
	}
	return nil
}

// Validate 只检查「给了没有」，**形状规则留给 service 层**。
//
// 用户名允许哪些字符是一条会演进的业务规则，把它同时写在这里和 service 里，
// 两处迟早会漂移——读者也就无法确定哪一处才是权威。这里只做边界上的
// 「非空」检查，真正的校验由 service.normalizeAndValidateUsername 负责，
// 它返回的 ValidationError 同样会被映射成 400。
func (r *RegisterRequest) Validate() error {
	r.Nickname = strings.TrimSpace(r.Nickname)
	r.Username = strings.TrimSpace(r.Username)
	if r.Nickname == "" {
		return fmt.Errorf("nickname is required")
	}
	if len([]rune(r.Nickname)) > model.MaxCommentNicknameLength {
		return fmt.Errorf("nickname must be at most %d characters", model.MaxCommentNicknameLength)
	}
	if r.Username == "" {
		return fmt.Errorf("username is required")
	}
	if strings.TrimSpace(r.Password) == "" {
		return fmt.Errorf("password is required")
	}
	if strings.TrimSpace(r.Email) == "" || strings.TrimSpace(r.Code) == "" {
		return fmt.Errorf("email and code are required")
	}
	return nil
}

func (r *ResetPasswordRequest) Validate() error {
	if strings.TrimSpace(r.Email) == "" || strings.TrimSpace(r.Code) == "" {
		return fmt.Errorf("email and code are required")
	}
	if strings.TrimSpace(r.Password) == "" {
		return fmt.Errorf("password is required")
	}
	return nil
}

func (r *ChangePasswordRequest) Validate() error {
	if strings.TrimSpace(r.CurrentPassword) == "" || strings.TrimSpace(r.NewPassword) == "" {
		return fmt.Errorf("current and new password are required")
	}
	return nil
}

// Validate 只确认「至少有一项要改」，具体取值由 service 层校验形状。
func (r *UpdateProfileRequest) Validate() error {
	if r.Username == nil && r.Nickname == nil {
		return fmt.Errorf("nothing to update")
	}
	trimmed := func(value *string) *string {
		if value == nil {
			return nil
		}
		text := strings.TrimSpace(*value)
		return &text
	}
	r.Username = trimmed(r.Username)
	r.Nickname = trimmed(r.Nickname)
	return nil
}

// Validate 校验展示名，并在给了登录名时套用与访客账号**同一套**用户名的形状规则。
//
// 登录名此前只检查了非空与长度，于是绕过前端的表单就能存进一个含零宽空格或
// 西里尔字母的名字——「用户名长什么样」不该取决于走哪个入口。规则本体在
// model.NormalizeAndValidateUsername，三处调用方共用那一份。
//
// 注意这里不校验「当前存储中的登录名」：老库里的值可能是按旧规则写的，
// 加载时不该因为不合法而拒绝启动；只有**改动**它时才要求符合规则。
func (r *AdminProfileRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len([]rune(r.Name)) > model.MaxCommentNicknameLength {
		return fmt.Errorf("name must be at most %d characters", model.MaxCommentNicknameLength)
	}
	if r.Username != nil {
		normalized, err := model.NormalizeAndValidateUsername(*r.Username)
		if err != nil {
			return err
		}
		r.Username = &normalized
	}
	return nil
}

func (r *AdminUpdateUserRequest) Validate() error {
	r.Username = strings.TrimSpace(r.Username)
	r.Nickname = strings.TrimSpace(r.Nickname)
	r.Email = strings.TrimSpace(r.Email)
	if r.Username == "" {
		return fmt.Errorf("username is required")
	}
	if r.Nickname == "" {
		return fmt.Errorf("nickname is required")
	}
	if len([]rune(r.Nickname)) > model.MaxCommentNicknameLength {
		return fmt.Errorf("nickname must be at most %d characters", model.MaxCommentNicknameLength)
	}
	if r.Email == "" {
		return fmt.Errorf("email is required")
	}
	return nil
}
