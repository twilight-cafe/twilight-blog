// Package session 实现基于 cookie、区分角色的会话，并附带 CSRF token。
//
// 设计要点：
//   - 原始会话 token 只以 httpOnly cookie 的形式发给浏览器，脚本读不到；
//     数据库里存的是它的 SHA-256 哈希，待遇和密码一样。
//   - 另有一个独立的 CSRF token，其哈希与会话存在同一行。它通过一个脚本
//     **可以读**的 cookie 下发，客户端必须把它放回 X-CSRF-Token 头。
//     跨站表单提交读不到那个 cookie，自然也就拼不出这个头。
//   - 会话是数据库里的一行，所以登出（或改密码）能真正做到**服务端失效**，
//     而不只是清掉浏览器上的 cookie。
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"blog-engine/internal/logging"
)

// Role 标识一个会话能做什么。
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// Cookie 名称。会话 cookie 是 httpOnly 的；CSRF cookie 则刻意允许页面脚本读取。
const (
	SessionCookieName = "tw_session"
	CSRFCookieName    = "tw_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

// 有效期。管理员会话偏短；访客账号会话更长，因为登录要走一次邮件验证码，
// 频繁失效的代价更高。
const (
	AdminTTL = 24 * time.Hour
	UserTTL  = 30 * 24 * time.Hour
)

// 两个哨兵错误，用于区分「根本没有这个会话」与「会话已过期」。
// Authenticate 中间件对两者都放行（视为未登录），只在其他错误时记日志。
var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionExpired  = errors.New("session expired")
)

// randRead is crypto/rand.Read behind a variable so tests can exercise the
// otherwise-impossible entropy-failure branches deterministically.
var randRead = rand.Read

// Session 是解析成功后的服务端会话。
type Session struct {
	TokenHash string
	Role      Role
	UserID    *int64
	ExpiresAt int64
}

// IsAdmin 报告该会话是否属于管理员。
func (s *Session) IsAdmin() bool { return s.Role == RoleAdmin }

// Store 提供会话的签发、解析、校验与吊销，全部以数据库为准。
//
// 它不做内存缓存：每次请求都要查一次库，换来的是「吊销立即生效」这个保证——
// 一旦加了缓存，「改密码后其他设备立刻下线」就不再成立了。
type Store struct {
	db *sql.DB
}

// NewStore 用给定的数据库连接构造会话存储。
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Create 签发一个新会话，并返回**原始** token。
// 原始值不会被持久化，库里只存它们的哈希。
func (s *Store) Create(ctx context.Context, role Role, userID *int64, ttl time.Duration) (token, csrf string, expiresAt int64, err error) {
	token, err = randomToken()
	if err != nil {
		return "", "", 0, err
	}
	csrf, err = randomToken()
	if err != nil {
		return "", "", 0, err
	}

	now := time.Now()
	expiresAt = now.Add(ttl).Unix()

	// 顺手清理过期会话，免得在两次定时清扫之间表无限增长。
	//
	// 必须在 INSERT **之前**执行：若放在后面，当 TTL 不是严格未来时间时，
	// 会把刚插入的这一行也一并删掉。清理失败只记 Warn——它不该阻挡登录。
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		logging.Warnf("[session] pruning expired sessions failed: %v", err)
	}

	if _, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (token_hash, role, user_id, csrf_hash, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?)
`, HashToken(token), string(role), userID, HashToken(csrf), expiresAt, now.Unix()); err != nil {
		return "", "", 0, fmt.Errorf("create session: %w", err)
	}

	return token, csrf, expiresAt, nil
}

// Lookup 用原始 token 解析会话；发现已过期时顺手删掉那一行。
//
// 它同时回查 users 表，确认会话行里的 role 与账号行里的 is_admin 一致。
// 这不是多余的：role 是**签发时复制进会话行的冗余副本**，而授权只看它
// （middleware.RequireAdmin 判的是 sess.IsAdmin()）。副本一旦与事实脱节，
// 授权结论就错了。两种真实脱节形态：
//   - 依赖迁移前的管理员会话：那时管理员没有 users 行，会话行的 user_id 是 NULL，
//     而迁移只搬凭据、不动 sessions，于是这行会话在迁移后**依然是管理员**；
//   - 账号被改写（is_admin 被手工改成 0，或行被删）：role='admin' 仍会放行。
//
// 不一致时按「未登录」处理并吊销该行，fail closed。
func (s *Store) Lookup(ctx context.Context, token string) (*Session, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrSessionNotFound
	}

	hash := HashToken(token)

	var (
		role       string
		userID     sql.NullInt64
		expiresAt  int64
		isAdminRow int
	)
	err := s.db.QueryRowContext(ctx, `
SELECT s.role, s.user_id, s.expires_at, COALESCE(u.is_admin, 0)
FROM sessions s
LEFT JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ?
`, hash).Scan(&role, &userID, &expiresAt, &isAdminRow)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("look up session: %w", err)
	}

	if time.Now().Unix() >= expiresAt {
		if _, delErr := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash); delErr != nil {
			logging.Warnf("[session] deleting expired session failed: %v", delErr)
		}
		return nil, ErrSessionExpired
	}

	if !roleMatchesAccount(Role(role), userID, isAdminRow) {
		logging.Warnf("[session] revoking a session whose role no longer matches its account role=%s user_id_set=%t is_admin=%d",
			role, userID.Valid, isAdminRow)
		if _, delErr := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash); delErr != nil {
			logging.Warnf("[session] revoking the mismatched session failed: %v", delErr)
		}
		return nil, ErrSessionNotFound
	}

	resolved := &Session{TokenHash: hash, Role: Role(role), ExpiresAt: expiresAt}
	if userID.Valid {
		value := userID.Int64
		resolved.UserID = &value
	}
	return resolved, nil
}

// roleMatchesAccount 判断会话行声明的角色与账号行的实际身份是否一致。
//
// 会话必须指向一个具体账号（user_id 非空），且 is_admin 要与之对应：
// 管理员会话要求 is_admin = 1，访客会话要求 is_admin = 0。
// 未知角色一律不通过——sessions.role 没有 CHECK 约束，不能假定只有两个取值。
func roleMatchesAccount(role Role, userID sql.NullInt64, isAdminRow int) bool {
	if !userID.Valid {
		return false
	}
	switch role {
	case RoleAdmin:
		return isAdminRow == 1
	case RoleUser:
		return isAdminRow == 0
	default:
		return false
	}
}

// VerifyCSRF 以恒定时间比较客户端提交的 CSRF token 与会话绑定的那一个。
//
// 比较的是两个哈希值，所以即使数据库泄露，也拿不到可直接使用的 token。
// 会话不存在或提交为空一律返回 false（不报错），让调用方只处理「过/不过」。
func (s *Store) VerifyCSRF(ctx context.Context, sess *Session, presented string) bool {
	presented = strings.TrimSpace(presented)
	if sess == nil || presented == "" {
		return false
	}

	var stored string
	if err := s.db.QueryRowContext(ctx, `SELECT csrf_hash FROM sessions WHERE token_hash = ?`, sess.TokenHash).Scan(&stored); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logging.Errorf("[session] load csrf hash failed: %v", err)
		}
		return false
	}

	return subtle.ConstantTimeCompare([]byte(stored), []byte(HashToken(presented))) == 1
}

// Revoke 按原始 token 删除单个会话（登出用）。
// token 为空视为无事可做，返回 nil。
func (s *Store) Revoke(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, HashToken(token)); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeUserSessions 让某个用户的所有会话失效。
// 改密码、重置密码、删除账号之后都必须调用它，否则被盗用的会话仍然有效。
func (s *Store) RevokeUserSessions(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

// RevokeLegacySessions 删除所有没有指向具体账号的会话行，返回删除数量。
//
// 「user_id IS NULL」是**迁移前**的管理员会话形态：那时管理员没有 users 行，
// 签发会话时只能把 user_id 留空。这类行有两个必须处理的后果：
//
//   - middleware.RequireAdmin 只看 role，所以它们在凭据搬进 users 表之后
//     **依然被当成有效管理员会话**——一次凭据迁移没有作废任何旧会话；
//   - RevokeUserSessions 按 user_id 删，匹配不到 NULL，因此事后「改密码把其他
//     设备挤下线」对它们**静默无效**。被盗的旧 cookie 赶不走。
//
// 迁移是凭据轮换，轮换就必须让旧会话全部失效。main 在迁移之后、开始服务之前
// 调用本方法（见 main.go 的顺序注释）。每次启动都跑一遍是刻意的：它同时能
// 自愈那些被旧版本迁移过、因而残留了 NULL 行的数据库。
//
// 正常运行期间不会有 user_id 为 NULL 的会话：session.Create 的所有调用点都传
// 真实的账号 id（管理员也是 users 表里的一行）。
func (s *Store) RevokeLegacySessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("revoke legacy sessions: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("revoke legacy sessions rows affected: %w", err)
	}
	if removed > 0 {
		logging.Warnf("[session] revoked %d session(s) that predate the credential migration; "+
			"whoever held them must sign in again", removed)
	}
	return removed, nil
}

// RevokeAll 让全部会话失效（含管理员会话）。
// 管理员改密码后用它把所有已登录设备踢下线。
func (s *Store) RevokeAll(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return fmt.Errorf("revoke all sessions: %w", err)
	}
	return nil
}

// HashToken 返回原始 token 的十六进制 SHA-256。
//
// 这里不加盐、也不用慢哈希，与密码的处理刻意不同：token 是 32 字节随机数，
// 没有可猜测的分布，暴力枚举不可行；加盐反而会让「按哈希查一行」变得不可能，
// 而会话查找正需要这个能力。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// randomToken 生成 32 字节（256 位）密码学随机数，十六进制编码为 64 个字符。
// 必须用 crypto/rand：math/rand 的序列可预测，等于没有会话安全。
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := randRead(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

/* -------------------------------------------------------------------------- */
/* Cookie 传输                                                                */
/* -------------------------------------------------------------------------- */

// SetCookies 写入会话与 CSRF 两个 cookie。
//
// 会话 cookie 是 httpOnly + SameSite=Lax：Lax 已能阻止浏览器把它附到跨站 POST 上，
// 而 CSRF token 是第二道独立屏障（第三道是 middleware.OriginCheck）。
// Path 都设为 "/"，这样 API 与 SPA 都能看到 CSRF cookie。
func SetCookies(c echo.Context, secure bool, token, csrf string, expiresAt int64) {
	expires := time.Unix(expiresAt, 0)

	c.SetCookie(&http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	c.SetCookie(&http.Cookie{
		Name:    CSRFCookieName,
		Value:   csrf,
		Path:    "/",
		Expires: expires,
		// Readable by the SPA on purpose: the client must echo it in a header.
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookies 让两个 cookie 立即失效（过期时间设到过去，MaxAge = -1）。
// 即便当前请求没有有效会话也应当调用——目的是清掉浏览器上的残留值。
func ClearCookies(c echo.Context, secure bool) {
	past := time.Unix(0, 0)
	for _, name := range []string{SessionCookieName, CSRFCookieName} {
		c.SetCookie(&http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			Expires:  past,
			MaxAge:   -1,
			HttpOnly: name == SessionCookieName,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

// TokenFromRequest 返回请求携带的原始会话 token（取自 cookie），没有则返回空串。
func TokenFromRequest(c echo.Context) string {
	cookie, err := c.Cookie(SessionCookieName)
	if err != nil || cookie == nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

// CSRFFromRequest 返回客户端在请求头里提交的 CSRF token。
//
// 它**刻意不回退**去读 tw_csrf cookie。浏览器会自动带上那个 cookie，
// 一旦接受它，检查就变成了「拿 cookie 和它自己比」，永远通过——
// 而那正是这套机制本该提供的保护。那个 cookie 存在的唯一理由，
// 是让页面自己的脚本能读到值并放进请求头；跨站表单提交两件事都做不到。
func CSRFFromRequest(c echo.Context) string {
	return strings.TrimSpace(c.Request().Header.Get(CSRFHeaderName))
}

/* -------------------------------------------------------------------------- */
/* 请求上下文                                                                 */
/* -------------------------------------------------------------------------- */

// 请求上下文里存放已解析会话的键。用一个不导出的常量，避免与其他中间件撞键。
const contextKey = "session"

// Set 把解析好的会话放进请求上下文。
func Set(c echo.Context, sess *Session) { c.Set(contextKey, sess) }

// From 取出请求上下文里的会话；没有有效会话时返回 (nil, false)。
func From(c echo.Context) (*Session, bool) {
	sess, ok := c.Get(contextKey).(*Session)
	return sess, ok && sess != nil
}

// UserID 返回该会话绑定的账号 id；未登录时返回 nil。
//
// ⚠️ **语义已经变了，改动调用方前务必读完这一段。**
//
// 旧版对管理员返回 nil（管理员没有 users 行），于是不少代码把「nil」当作
// 「不是访客账号」来用。现在管理员**也有**真实的账号行（users 表里 is_admin = 1
// 那一行），所以 UserID 对管理员**非 nil**，那些判断会静默翻转——
// 不报错、不失败编译，只是授权结果变了。
//
// 正确的写法是按**角色**分流，而不是靠 UserID 是否为空：
//
//	if sess.IsAdmin() { ...管理员... } else if sess.UserID != nil { ...访客... }
//
// 需要「仅访客」的端点（PATCH /api/me、点赞/取消点赞）必须显式加 IsAdmin() 判断，
// 见 handler 层那几处的注释。
func UserID(c echo.Context) *int64 {
	sess, ok := From(c)
	if !ok {
		return nil
	}
	return sess.UserID
}

// IsAdmin 报告该请求是否以管理员身份通过了认证。
func IsAdmin(c echo.Context) bool {
	sess, ok := From(c)
	return ok && sess.IsAdmin()
}
