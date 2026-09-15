package model

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 登录标识（用户名）的形状规则——**全站唯一一份**。
//
// 放在 model 而不是 service，是因为有三个入口都要用它，而它们分属不同层：
//   - 访客注册 / 改名：service 层（原本在这里）；
//   - 管理员登录名：viewmodel 层的请求校验（config.SetAdminLoginName 只挡空值）；
//   - 管理员资料的接口：同上。
//
// 此前这条规则只存在于 service，于是管理员登录名只被检查了「非空」与「长度」，
// 可以直接调接口存进一个含零宽空格或西里尔字母的名字——「用户名长什么样」
// 变成了取决于走哪个入口的偶然结果。规则分叉的代价不止是难看：同形异义字符
// （西里尔字母 а 与拉丁字母 a 在多数界面里长得一模一样）正是冒充与钓鱼的温床。
//
// 刻意只允许 ASCII：不做大小写折叠，存储保留用户输入的大小写，唯一性与查找走
// COLLATE NOCASE，于是 "Alice" 与 "alice" 是同一个名字，而显示时用户看到的
// 仍是他自己写的那个。以字母开头可避免纯数字用户名与 id 混淆，
// 也避免开头就是 '-' 这种在 URL 与命令行里都要额外转义的形式。
var usernamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

const (
	// MinUsernameLength 是登录标识的最小长度。
	MinUsernameLength = 3
	// MaxUsernameLength 与展示名共用同一个上限，使「用户名」在列表里不会
	// 比昵称长出一截，排版不跳。
	MaxUsernameLength = MaxCommentNicknameLength
)

// NormalizeAndValidateUsername 去空白、检查形状与长度，返回**原样大小写**的名字。
//
// 错误文案会被原样回给客户端（service 会把它包成 ValidationError），
// 所以改这些字符串等于改 API 响应，不是纯内部重构。
func NormalizeAndValidateUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", fmt.Errorf("username is required")
	}
	if n := utf8.RuneCountInString(username); n < MinUsernameLength || n > MaxUsernameLength {
		return "", fmt.Errorf("username must be between %d and %d characters", MinUsernameLength, MaxUsernameLength)
	}
	if !usernamePattern.MatchString(username) {
		return "", fmt.Errorf("username must start with a letter and contain only letters, digits, underscores and hyphens")
	}
	return username, nil
}
