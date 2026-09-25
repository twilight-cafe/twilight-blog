package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/database"
	"blog-engine/internal/model"
)

// captureSender records the codes that would have been emailed so the test can
// complete the flow without a mail provider.
type captureSender struct {
	to     string
	codes  []string
	bodies []string
}

// codePattern 从假发信器拿到的正文里取出验证码。
//
// 位数必须与 generateVerificationCode 一致（当前 8 位）。它一旦漂移，测试会以
// 「verification code is required」失败——那是刻意的：这种失败比「正则悄悄匹配到
// 正文里别的数字、于是测了个寂寞」容易查得多。
var codePattern = regexp.MustCompile(`\b(\d{8})\b`)

func (s *captureSender) Send(to, _, body string) error {
	s.capture(to, body)
	return nil
}

// SendVerification 与 Send 一样记下正文：这个假发信器模拟的是「信被发出去了」，
// 而**留痕打码是 EmailService 的职责**（见 email_service_test.go 里对
// redactSecret 的测试）。在这里也打码的话，测试就看不到验证码、流程走不下去，
// 那才是把被测行为搬到了测试替身里。
func (s *captureSender) SendVerification(to, _, body, _ string) error {
	s.capture(to, body)
	return nil
}

func (s *captureSender) capture(to, body string) {
	s.to = to
	s.bodies = append(s.bodies, body)
	if match := codePattern.FindStringSubmatch(body); match != nil {
		s.codes = append(s.codes, match[1])
	}
}

func (s *captureSender) latest() string {
	if len(s.codes) == 0 {
		return ""
	}
	return s.codes[len(s.codes)-1]
}

// lastBody 返回最近一次发信的**完整正文**。latest() 只存验证码本身
// （注册流程只需要它），断言文案的测试（验证码邮件中文化）才需要正文。
func (s *captureSender) lastBody() string {
	if len(s.bodies) == 0 {
		return ""
	}
	return s.bodies[len(s.bodies)-1]
}

// testEmailCodeInterval 是测试里生效的短间隔节流窗口，**刻意比真实值短**。
//
// 真实值是 60 秒（见 emailCodeMinInterval）。账号流程里「为同一个地址连发两枚码」
// 是常态——注册被拒后换名字重试、重置密码、验证码按地址计数——照真实值走，
// service 包会因为这些等待多出十几分钟。
//
// 但缩短窗口**只是把等待变小，不是把它去掉**，这一点很关键：测试仍然走真实的
// 节流判定路径（LastCreatedAt → 比较间隔）。凡是需要连发两枚码的地方都必须显式
// 调用 advancePastEmailCodeInterval 推进「签发时间」；忘了推进，就会像以前那样
// 拿到 ErrEmailCodeTooFrequent 而失败。
//
// ⚠️ 为什么是 2 秒而不是 200 毫秒：created_at 是 **Unix 秒**（整秒）落库的，
// 而节流把两侧都换算成秒再比较。窗口一旦短于一秒，`interval.Seconds()` 会截断成
// 0，节流就**永远不会触发**——测试会静默地变成什么都没测。2 秒既能保证换算后
// 是正整数（2），又远短于 60 秒，等待代价可以忽略。
const testEmailCodeInterval = 2 * time.Second

// advancePastEmailCodeInterval 让指定地址看起来「上一次签发已经过去足够久」，
// 从而允许它再申请一枚码。必须在同一地址的第二次 RequestEmailCode 之前调用。
//
// 做法是把库里该地址的 created_at **往前挪**，而不是等真实时间流逝；节流判定
// 本身（LastCreatedAt → 比较间隔）原样运行，所以测试既省掉了等待，又没有把
// 被测逻辑换掉。
//
// ⚠️ 挪的方向必须是**过去**，且要锚定在真实的 time.Now() 上。RequestEmailCode
// 写入的是 time.Now()，若这里写一个比它更大的值（比如自己累加出来的时钟），
// created_at 就落到「未来」，而节流恰好有一条「last > now 时放行」的回拨保护
// ——测试会因此看起来通过，实际上一次都没走到节流判定上。
func advancePastEmailCodeInterval(t *testing.T, db *sql.DB, email string) {
	t.Helper()

	// 现在就有一个足够久远的过去时刻：真实当前时间再往前 interval+1 秒。
	// 每次调用都重新基于 time.Now() 计算，所以不会累积、也不可能超过 now。
	past := time.Now().Add(-(testEmailCodeInterval + time.Second)).Unix()
	if _, err := db.Exec(`
UPDATE email_verification_codes SET created_at = ? WHERE email = ? COLLATE NOCASE
`, past, email); err != nil {
		t.Fatalf("advance the verification code clock for %s: %v", email, err)
	}
}

// newTestUserService 构造一个只依赖访客账号体系的服务。
//
// 从前这里还要注入一个「站长当前登录名」的函数，好让 isReservedUsername 把站长的
// 名字预留出来。那次重构之后不需要了：站长也是 users 表里的一行，与访客共用同一个
// 部分唯一索引——测试因此改成「插一行 is_admin = 1，再验证唯一性被数据库守住」。
//
// 服务的节流窗口被压到 testEmailCodeInterval，见那个常量的说明。
func newTestUserService(t *testing.T) (*UserService, *captureSender, *sql.DB) {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	sender := &captureSender{}
	svc := NewUserService(
		model.NewSQLiteUserRepository(db),
		model.NewSQLiteEmailVerificationRepository(db),
		sender,
	)
	svc.emailCodeIntervalOverride = testEmailCodeInterval
	return svc, sender, db
}

// seedAdministrator 在 users 表里插入唯一的管理员行。
//
// 用户名是参数而不是常量：这一版里「管理员的名字」不再是一个固定的预留值，
// 它就是这张表里的一个普通值，只是那一行的 is_admin 为 1。
func seedAdministrator(t *testing.T, db *sql.DB, username string) *model.User {
	t.Helper()

	admin := &model.User{
		// 空串哨兵：任何注册路径都产生不了这个地址。
		Email:        "",
		Username:     username,
		Nickname:     username,
		PasswordHash: "$2a$10$AfQeX1gjl448zVrqV29jW.bMN8HjbLVIJdrSGsa7H39bOO.pm7da.",
		CreatedAt:    1,
		UpdatedAt:    1,
	}
	if err := model.NewSQLiteAdminRepository(db).Create(context.Background(), admin); err != nil {
		t.Fatalf("seed administrator %q: %v", username, err)
	}
	return admin
}

// register 走完整流程：申请验证码 → 用最新的一枚注册。
//
// 每个测试用例都把 username 与 nickname 传成**不同的值**，因为两者必须是
// 两个独立的字段，测试里若让它们相等，就无法区分「分开存的」与「本来就是
// 同一个字符串」。
//
// 申请之前先推进节流时钟：调用方可能已经为同一个地址要过一枚码
// （比如「注册一次 → 再注册一次」），而真实节流是 60 秒。
func register(t *testing.T, svc *UserService, sender *captureSender, db *sql.DB, email, username, password, nickname string) *model.User {
	t.Helper()
	ctx := context.Background()

	advancePastEmailCodeInterval(t, db, email)
	if err := svc.RequestEmailCode(ctx, email); err != nil {
		t.Fatalf("RequestEmailCode(%s) error = %v", email, err)
	}
	user, err := svc.Register(ctx, email, sender.latest(), username, nickname, password)
	if err != nil {
		t.Fatalf("Register(%s) error = %v", email, err)
	}
	return user
}

func TestRegisterThenLogin(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	if err := svc.RequestEmailCode(ctx, "New@Example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	if sender.to != "new@example.com" {
		t.Fatalf("code sent to %q, want the normalised address", sender.to)
	}

	// 上面已经为这个地址要过一枚码（地址归一化也在这里被验证），而 register 会
	// 再要一枚——所以要先把节流时钟推过去，否则第二枚会被 60 秒闸门拦下。
	advancePastEmailCodeInterval(t, db, "new@example.com")
	user := register(t, svc, sender, db, "new@example.com", "nightwalker", "GoodPass123", "夜行者")
	if user.ID == 0 || user.Email != "new@example.com" || user.Nickname != "夜行者" {
		t.Fatalf("unexpected user: %+v", user)
	}
	if user.Username != "nightwalker" {
		t.Fatalf("username = %q, want nightwalker", user.Username)
	}
	if !user.HasPassword() {
		t.Fatal("registration must store a password hash")
	}

	loggedIn, err := svc.LoginWithUsername(ctx, "nightwalker", "GoodPass123")
	if err != nil {
		t.Fatalf("LoginWithUsername() error = %v", err)
	}
	if loggedIn.ID != user.ID {
		t.Fatalf("logged in as %d, want %d", loggedIn.ID, user.ID)
	}

	// The login identifier is matched case-insensitively, like the unique index.
	if _, err := svc.LoginWithUsername(ctx, "NightWalker", "GoodPass123"); err != nil {
		t.Fatalf("case-insensitive login failed: %v", err)
	}

	// The registered address keeps working as a compatibility entry point, so a
	// visitor who predates usernames is never locked out.
	if _, err := svc.LoginWithUsername(ctx, "NEW@example.com", "GoodPass123"); err != nil {
		t.Fatalf("login by registered address failed: %v", err)
	}
}

// TestUsernameAndNicknameAreSeparate is the guard for the rule that the two
// fields must never stand in for each other.
//
// It asserts on the *persisted* values after each kind of edit, because the
// failure mode being guarded against is silent: a single shared column would
// still make every assertion about "the display name" pass.
func TestUsernameAndNicknameAreSeparate(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	user := register(t, svc, sender, db, "sep@example.com", "sepuser", "GoodPass123", "展示名")

	// Changing the display name must not move the login identifier.
	if _, err := svc.UpdateOwnNickname(ctx, user.ID, "改过的展示名"); err != nil {
		t.Fatalf("UpdateOwnNickname() error = %v", err)
	}
	afterRename, err := svc.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if afterRename.Nickname != "改过的展示名" {
		t.Fatalf("nickname = %q, want 改过的展示名", afterRename.Nickname)
	}
	if afterRename.Username != "sepuser" {
		t.Fatalf("username changed to %q while only the nickname was edited", afterRename.Username)
	}
	if _, err := svc.LoginWithUsername(ctx, "sepuser", "GoodPass123"); err != nil {
		t.Fatalf("login stopped working after a nickname change: %v", err)
	}

	// Changing the login identifier must not move the display name either.
	if _, err := svc.UpdateOwnUsername(ctx, user.ID, "renamed-user"); err != nil {
		t.Fatalf("UpdateOwnUsername() error = %v", err)
	}
	afterUserRename, err := svc.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if afterUserRename.Nickname != "改过的展示名" {
		t.Fatalf("nickname = %q while only the username was edited", afterUserRename.Nickname)
	}
	if _, err := svc.LoginWithUsername(ctx, "renamed-user", "GoodPass123"); err != nil {
		t.Fatalf("login with the new username failed: %v", err)
	}
	// The old identifier must be gone, otherwise "renaming" would silently keep
	// a second way into the account.
	if _, err := svc.LoginWithUsername(ctx, "sepuser", "GoodPass123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("the previous username still signs in (err=%v)", err)
	}
}

// TestUsernameUniqueness covers the constraint the login path depends on:
// two accounts may never share a username, and the collision must be reported
// as a username conflict rather than as an email conflict.
func TestUsernameUniqueness(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	register(t, svc, sender, db, "first@example.com", "taken-name", "GoodPass123", "第一个")

	if err := svc.RequestEmailCode(ctx, "second@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	_, err := svc.Register(ctx, "second@example.com", sender.latest(), "TAKEN-NAME", "第二个", "GoodPass456")
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate username error = %v, want ErrUsernameTaken", err)
	}

	// 被拒绝的注册会**消费掉**那枚码（这是有意为之，理由见 Register 的注释与
	// TestRejectedRegistrationConsumesTheCode），所以换名字重试之前要重新申请一枚。
	advancePastEmailCodeInterval(t, db, "second@example.com")
	if err := svc.RequestEmailCode(ctx, "second@example.com"); err != nil {
		t.Fatalf("second RequestEmailCode() error = %v", err)
	}
	user, err := svc.Register(ctx, "second@example.com", sender.latest(), "free-name", "第二个", "GoodPass456")
	if err != nil {
		t.Fatalf("Register() after choosing a free username error = %v", err)
	}
	if user.Username != "free-name" {
		t.Fatalf("username = %q, want free-name", user.Username)
	}

	// Renaming onto someone else's identifier is rejected too, and the rejected
	// account keeps its own name.
	if _, err := svc.UpdateOwnUsername(ctx, user.ID, "taken-name"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("rename onto a taken username error = %v, want ErrUsernameTaken", err)
	}
	unchanged, err := svc.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if unchanged.Username != "free-name" {
		t.Fatalf("username = %q after a rejected rename, want free-name", unchanged.Username)
	}

	// Renaming to a differently-cased spelling of one's own name is a no-op,
	// not a self-collision.
	if _, err := svc.UpdateOwnUsername(ctx, user.ID, "Free-Name"); err != nil {
		t.Fatalf("renaming to a different case of one's own username failed: %v", err)
	}
}

// TestAdministratorLoginNameIsReserved covers the rule that makes a single
// sign-in path possible: the administrator's login name and visitor usernames are
// one namespace, because sign-in tells the two identities apart by username alone
// (see handler.AuthHandler.Login). If a visitor could take the administrator's
// name, sign-in would resolve that name to the administrator and the visitor
// account would exist while being unable to ever sign in.
//
// Every write path is exercised on purpose: one path that forgot the rule is
// enough to create the collision, and the other paths would then keep it alive.
//
// 与旧版最大的区别：管理员的名字不再是「settings.json 里的一个字符串」，而是
// users 表里 is_admin = 1 那一行的 username。于是这条规则不再由应用层的预留判定
// 提供，而是由数据库上的部分唯一索引（idx_users_username）提供——所以这个测试
// 先插一行管理员，再验证三条写入路径都被同一个索引拦下。
func TestAdministratorLoginNameIsReserved(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	seedAdministrator(t, db, "admin")

	// Registration refuses the name in any casing. 拒绝发生在验证码消费**之后**，
	// 所以下面换名字时需要一枚新码——这是「校验即消费」的既定代价。
	if err := svc.RequestEmailCode(ctx, "reserved@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	if _, err := svc.Register(ctx, "reserved@example.com", sender.latest(), "ADMIN", "候选人", "GoodPass123"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("Register() with the administrator login name error = %v, want ErrUsernameTaken", err)
	}
	advancePastEmailCodeInterval(t, db, "reserved@example.com")
	if err := svc.RequestEmailCode(ctx, "reserved@example.com"); err != nil {
		t.Fatalf("second RequestEmailCode() error = %v", err)
	}
	user, err := svc.Register(ctx, "reserved@example.com", sender.latest(), "not-the-admin", "候选人", "GoodPass123")
	if err != nil {
		t.Fatalf("Register() with a free username error = %v", err)
	}

	// Self-service rename and the administrator's edit of someone else's account
	// must refuse it too.
	if _, err := svc.UpdateOwnUsername(ctx, user.ID, "admin"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("UpdateOwnUsername() onto the administrator login name error = %v, want ErrUsernameTaken", err)
	}
	if _, err := svc.AdminUpdateUser(ctx, user.ID, "Admin", "候选人", "reserved@example.com"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("AdminUpdateUser() onto the administrator login name error = %v, want ErrUsernameTaken", err)
	}
	unchanged, err := svc.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if unchanged.Username != "not-the-admin" {
		t.Fatalf("username = %q after two rejected writes, want not-the-admin", unchanged.Username)
	}

	// 反方向：管理员不能把名字改到访客的用户名上（同样由索引拦下，
	// 见 model.TestAdminUsernameSharesTheVisitorNamespace）。这里只确认
	// 访客的用户名确实在同一个命名空间里可达。
	if _, err := svc.UpdateOwnUsername(ctx, user.ID, "Not-The-Admin"); err != nil {
		t.Fatalf("renaming to a different case of one's own username failed: %v", err)
	}
}

// TestUsernameShapeIsValidated pins the accepted identifier shape, because the
// rule is what keeps confusable Unicode look-alikes out of a name that people
// read on a sign-in screen.
func TestUsernameShapeIsValidated(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	cases := []struct {
		name     string
		username string
	}{
		{"empty", ""},
		{"too short", "ab"},
		{"too long", "abcdefghijklmnopqrstuvwxy"},
		{"leading digit", "1abc"},
		{"leading underscore", "_abc"},
		{"space inside", "abc def"},
		{"cyrillic look-alike", "аdmin"},
		{"dot", "a.b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 每个子用例都会为同一个地址再要一枚码，所以每次都要推进节流时钟。
			advancePastEmailCodeInterval(t, db, "shape@example.com")
			if err := svc.RequestEmailCode(ctx, "shape@example.com"); err != nil {
				t.Fatalf("RequestEmailCode() error = %v", err)
			}
			if _, err := svc.Register(ctx, "shape@example.com", sender.latest(), tc.username, "昵称", "GoodPass123"); err == nil {
				t.Fatalf("username %q was accepted", tc.username)
			}
		})
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	svc, sender, _ := newTestUserService(t)
	ctx := context.Background()

	if err := svc.RequestEmailCode(ctx, "a@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	good := sender.latest()

	cases := []struct {
		name     string
		email    string
		code     string
		username string
		password string
		nickname string
	}{
		{"wrong code", "a@example.com", "00000000", "nickname1", "GoodPass123", "nick"},
		{"empty code", "a@example.com", "", "nickname1", "GoodPass123", "nick"},
		{"weak password", "a@example.com", good, "nickname1", "short", "nick"},
		{"password without digit", "a@example.com", good, "nickname1", "onlyletters", "nick"},
		{"missing nickname", "a@example.com", good, "nickname1", "GoodPass123", ""},
		{"missing username", "a@example.com", good, "", "GoodPass123", "nick"},
		{"invalid email", "not-an-email", good, "nickname1", "GoodPass123", "nick"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Register(ctx, tc.email, tc.code, tc.username, tc.nickname, tc.password); err == nil {
				t.Fatal("Register() accepted invalid input")
			}
		})
	}
}

// TestOnlyNewestCodeIsValid covers retiring outstanding codes when a new one is
// issued: otherwise each request would add another simultaneously valid code.
func TestOnlyNewestCodeIsValid(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	if err := svc.RequestEmailCode(ctx, "a@example.com"); err != nil {
		t.Fatalf("first RequestEmailCode() error = %v", err)
	}
	first := sender.latest()

	// 「签发新码即淘汰旧码」要的是两枚码，而第二枚会被短间隔节流拦下——
	// 节流本身由 TestVerificationCodeMinIntervalIsEnforced 覆盖，这里只想
	// 拿到两枚码，所以先把时钟推过间隔。
	advancePastEmailCodeInterval(t, db, "a@example.com")
	if err := svc.RequestEmailCode(ctx, "a@example.com"); err != nil {
		t.Fatalf("second RequestEmailCode() error = %v", err)
	}
	second := sender.latest()

	if first == second {
		t.Skip("the generator produced the same code twice; nothing to assert")
	}

	if _, err := svc.Register(ctx, "a@example.com", first, "newest-code-user", "nick", "GoodPass123"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("the superseded code was accepted (err=%v)", err)
	}
	if _, err := svc.Register(ctx, "a@example.com", second, "newest-code-user", "nick", "GoodPass123"); err != nil {
		t.Fatalf("the newest code was rejected: %v", err)
	}
}

// TestVerificationCodeIsCappedPerAddress is the regression test for the
// unauthenticated email-code endpoint being usable to flood one inbox: per-IP
// throttling is defeated by switching IP, so the limit has to exist per address.
func TestVerificationCodeIsCappedPerAddress(t *testing.T) {
	svc, _, db := newTestUserService(t)
	ctx := context.Background()

	for i := 0; i < emailCodesPerHourPerAddress; i++ {
		// 这里考的是**每小时**的上限，短间隔节流只会碍事，所以每一轮都把
		// 签发时间推旧（每轮推进 1 秒以上，10 轮仍在一小时窗口内）。
		advancePastEmailCodeInterval(t, db, "victim@example.com")
		if err := svc.RequestEmailCode(ctx, "victim@example.com"); err != nil {
			t.Fatalf("request %d error = %v, want success", i+1, err)
		}
	}
	if err := svc.RequestEmailCode(ctx, "victim@example.com"); !errors.Is(err, ErrEmailCodeAddressLimit) {
		t.Fatalf("request beyond the cap error = %v, want ErrEmailCodeAddressLimit", err)
	}

	// The allowance is per address, not global: one flooded inbox must not stop
	// everybody else from registering.
	if err := svc.RequestEmailCode(ctx, "other@example.com"); err != nil {
		t.Fatalf("a different address was blocked: %v", err)
	}

	// Consuming the codes must not hand the allowance back: otherwise
	// "use one, ask for another" would be an unbounded loop around the cap.
	if _, err := db.ExecContext(ctx, `
UPDATE email_verification_codes SET consumed_at = ? WHERE email = ?
`, time.Now().Unix(), "victim@example.com"); err != nil {
		t.Fatalf("mark codes consumed: %v", err)
	}
	if err := svc.RequestEmailCode(ctx, "victim@example.com"); !errors.Is(err, ErrEmailCodeAddressLimit) {
		t.Fatalf("after consuming the codes, error = %v, want the cap to still apply", err)
	}

	// An address written in a different case is the same inbox.
	if err := svc.RequestEmailCode(ctx, "Victim@Example.com"); !errors.Is(err, ErrEmailCodeAddressLimit) {
		t.Fatalf("case-varied address error = %v, want the cap to apply to the same inbox", err)
	}
}

// TestVerificationCodeMinIntervalIsEnforced 是本轮新增功能（验证码最短发送间隔）
// 的**核心守护**：同一收件地址连发两枚码，第二枚必须被拒。
//
// 它挡的是「连点」与「脚本高频重发」——按 IP 的限流换 IP 就绕过了，而这个闸门
// 按收件地址生效，是绕过前端直接打接口时唯一还能拦住连发的东西。
//
// 反证方式：删掉 RequestEmailCode 里的短间隔判定（或把 interval 改成 0），
// 第二个 RequestEmailCode 会成功，测试立刻失败。
func TestVerificationCodeMinIntervalIsEnforced(t *testing.T) {
	svc, _, db := newTestUserService(t)
	ctx := context.Background()

	if err := svc.RequestEmailCode(ctx, "throttle@example.com"); err != nil {
		t.Fatalf("first RequestEmailCode() error = %v", err)
	}

	// 立刻再要一枚：必须被拒，且是**节流**错误而不是别的错误——若退化成
	// 按地址上限，文案会指向「一小时后重试」，对用户是错误指引。
	if err := svc.RequestEmailCode(ctx, "throttle@example.com"); !errors.Is(err, ErrEmailCodeTooFrequent) {
		t.Fatalf("immediate resend error = %v, want ErrEmailCodeTooFrequent", err)
	}

	// 被拒的请求不该动用户手上那枚仍然有效的码：号码是不重号的一张凭证，
	// 一次失败的连点不该把它作废掉（否则「点快了反而连原来那枚也没了」）。
	repo := model.NewSQLiteEmailVerificationRepository(db)
	if _, err := repo.FindLatestActiveByEmail(ctx, "throttle@example.com", time.Now().Unix()); err != nil {
		t.Fatalf("the first code did not survive the throttled request: %v", err)
	}

	// 大小写变体是同一个收件箱，不能成为绕过节流的口子。
	if err := svc.RequestEmailCode(ctx, "THROTTLE@example.com"); !errors.Is(err, ErrEmailCodeTooFrequent) {
		t.Fatalf("case-varied immediate resend error = %v, want ErrEmailCodeTooFrequent", err)
	}

	// 另一个地址不受影响：节流按收件地址，不是全局的。
	if err := svc.RequestEmailCode(ctx, "someone-else@example.com"); err != nil {
		t.Fatalf("a different address was throttled: %v", err)
	}
}

// TestVerificationCodeThrottleSurvivesAClockRollback 是**时钟回拨**的守护测试。
//
// 背景：判定写成「now-last < interval」是不够的。若系统时间被 NTP 往回拨，
// 库里的「上次签发」就会落在 now **之后**，差值为负——而负数是「小于 interval」
// 的，于是用户被锁在节流里，且**每过一秒 remaining 反而变大**，等于永久发不出
// 验证码。改动前的注释就写着「差值为负，直接放行」，代码却少了那一步。
//
// 反证方式：把判定改回 `now.Unix()-last < interval`（去掉 `last <= now.Unix()`
// 那半句），这个测试会失败——被拒绝而不是放行。
func TestVerificationCodeThrottleSurvivesAClockRollback(t *testing.T) {
	svc, _, db := newTestUserService(t)
	ctx := context.Background()

	if err := svc.RequestEmailCode(ctx, "rollback@example.com"); err != nil {
		t.Fatalf("first RequestEmailCode() error = %v", err)
	}

	// 模拟回拨：把「上次签发」改到未来一分钟。此刻 now-last = -60，
	// 而 -60 < interval 恒成立——正是那条会把人锁死的路径。
	future := time.Now().Add(time.Minute).Unix()
	if _, err := db.Exec(`
UPDATE email_verification_codes SET created_at = ? WHERE email = ? COLLATE NOCASE
`, future, "rollback@example.com"); err != nil {
		t.Fatalf("move the last issue time into the future: %v", err)
	}

	// 回拨期间宁可宽松一次：必须放行，而不是拒绝。
	if err := svc.RequestEmailCode(ctx, "rollback@example.com"); err != nil {
		t.Fatalf("a code was refused while the clock was rolled back: %v; "+
			"a negative gap means the last issue time is in the future, "+
			"and refusing there locks the user out permanently", err)
	}
}

// TestVerificationCodeIntervalDefaultsToTheProductionValue 钉住**生产默认值本身**。
//
// 与 TestVerificationCodeMinIntervalIsEnforced 的分工：那条测「判定确实生效」，
// 这条测「生效的窗口是 60 秒」。两者缺一不可——把常量改成 1 秒，前者照样绿，
// 而防连点实际上已经形同虚设（人手速就能连发）。
//
// 同时确认测试用的可注入窗口**没有泄漏到默认路径**：裸 NewUserService 必须回落到
// 常量，否则「测试里缩短窗口」这个口子会在生产里静默生效。
func TestVerificationCodeIntervalDefaultsToTheProductionValue(t *testing.T) {
	svc := &UserService{}
	if got := svc.emailCodeInterval(); got != 60*time.Second {
		t.Fatalf("the default interval = %v, want 60s", got)
	}
	if got := svc.emailCodeInterval(); got != emailCodeMinInterval {
		t.Fatalf("the default interval = %v, which drifts from emailCodeMinInterval = %v",
			got, emailCodeMinInterval)
	}

	// 注入后的窗口仍然必须是「正的」：0 或负值会被 emailCodeInterval 当成
	// 「用默认值」，所以一个 0 值的注入是**静默无效**的——测试若这么写，
	// 它测的就是 60 秒而不是它以为的短窗口。
	svc.emailCodeIntervalOverride = testEmailCodeInterval
	if got := svc.emailCodeInterval(); got != testEmailCodeInterval {
		t.Fatalf("the overridden interval = %v, want %v", got, testEmailCodeInterval)
	}
}

// TestVerificationCodeSurvivesAFewTypos guards the other side of the attempt cap:
// a person who mistypes the code once or twice must still be able to register, so
// the code may only be burned once the allowance is actually exhausted.
func TestVerificationCodeSurvivesAFewTypos(t *testing.T) {
	svc, sender, _ := newTestUserService(t)
	ctx := context.Background()

	if err := svc.RequestEmailCode(ctx, "typo@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	code := sender.latest()

	for i := 0; i < maxVerificationCodeAttempts-1; i++ {
		if _, err := svc.Register(ctx, "typo@example.com", "00000000", "typo-user", "nick", "GoodPass123"); !errors.Is(err, ErrInvalidVerificationCode) {
			t.Fatalf("wrong attempt %d error = %v, want ErrInvalidVerificationCode", i+1, err)
		}
	}

	if _, err := svc.Register(ctx, "typo@example.com", code, "typo-user", "nick", "GoodPass123"); err != nil {
		t.Fatalf("the correct code was rejected after only %d wrong attempts: %v", maxVerificationCodeAttempts-1, err)
	}
}

// failingSender 模拟邮件服务不可用（未配置密钥、凭据过期、上游 5xx）。
type failingSender struct{ err error }

func (s *failingSender) Send(_, _, _ string) error                { return s.err }
func (s *failingSender) SendVerification(_, _, _, _ string) error { return s.err }

// TestUnsentVerificationCodeIsInvalidated 是本轮新增的守护测试（R3-07）。
//
// 改动前的顺序是「落库 → 发信」，发信失败只返回错误，那枚码却留在库里并且**仍然有效**
// （TTL 十分钟）。于是库里会存在一枚「唯一有权使用它的收件人从未收到」的验证码：
// 合法用户拿不到，而任何读到库的人（数据库、备份、WAL）可以拿去重置别人的密码。
// 也就是说，一次发信故障会静默地把验证码变成只对攻击者可见的凭据。
func TestUnsentVerificationCodeIsInvalidated(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "unsent.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	sendErr := errors.New("email sending is disabled")
	svc := NewUserService(
		model.NewSQLiteUserRepository(db),
		model.NewSQLiteEmailVerificationRepository(db),
		&failingSender{err: sendErr},
	)
	ctx := context.Background()

	if err := svc.RequestEmailCode(ctx, "victim@example.com"); !errors.Is(err, sendErr) {
		t.Fatalf("RequestEmailCode() error = %v, want the sender error", err)
	}

	repo := model.NewSQLiteEmailVerificationRepository(db)
	if _, err := repo.FindLatestActiveByEmail(ctx, "victim@example.com", time.Now().Unix()); !errors.Is(err, model.ErrVerificationCodeNotFound) {
		t.Fatalf("an unsent code is still active (err=%v); a failed send must invalidate it", err)
	}
}

// TestVerificationCodeIsBurnedAfterRepeatedFailures is the regression test for
// brute-forcing a verification code inside its ten-minute window: per-IP throttling
// does not bound a distributed attacker, so the code itself must stop accepting
// guesses. The assertion that matters is the last one — a *correct* code must be
// refused once the allowance is gone.
func TestVerificationCodeIsBurnedAfterRepeatedFailures(t *testing.T) {
	svc, sender, _ := newTestUserService(t)
	ctx := context.Background()

	if err := svc.RequestEmailCode(ctx, "brute@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	code := sender.latest()

	// Hundred-million-to-one space, so a code that is not the answer is a safe
	// wrong guess: all-zeroes can only collide if the generator really produced it.
	if code == "00000000" {
		t.Skip("the generator produced the placeholder code; nothing to assert")
	}

	for i := 0; i < maxVerificationCodeAttempts; i++ {
		if _, err := svc.Register(ctx, "brute@example.com", "00000000", "brute-user", "nick", "GoodPass123"); !errors.Is(err, ErrInvalidVerificationCode) {
			t.Fatalf("wrong attempt %d error = %v, want ErrInvalidVerificationCode", i+1, err)
		}
	}

	if _, err := svc.Register(ctx, "brute@example.com", code, "brute-user", "nick", "GoodPass123"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("after %d failed attempts the correct code was still accepted (err=%v)", maxVerificationCodeAttempts, err)
	}

	// The account must not have been created along the way, and asking for a fresh
	// code is the legitimate way forward.
	if _, err := svc.users.FindByEmail(ctx, "brute@example.com"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("an account exists after only failed attempts: %v", err)
	}
}

// reservingRepoStub 把 ReserveAttempt 的结果固定成 refuse，其余方法全部透传。
// 用它来**确定性地**验证「预留是验证的前置条件」这条顺序约束——不需要真的并发。
type reservingRepoStub struct {
	model.EmailVerificationRepository
	refuse bool
	calls  int
}

func (s *reservingRepoStub) ReserveAttempt(ctx context.Context, id int64, limit int) (bool, error) {
	s.calls++
	if s.refuse {
		return false, nil
	}
	return s.EmailVerificationRepository.ReserveAttempt(ctx, id, limit)
}

// TestReservationIsAPreconditionForVerification 钉住 consumeCode 里的**调用顺序**：
// 必须「先原子预留一次猜测机会、再比较哈希」，而不是「先比较、失败后再计数」。
//
// 为什么要专门为顺序写一条测试：两种写法在串行下表现**完全一致**，只有并发才分得出来。
// 原先的写法（先比较、失败后递增）让 N 个并发请求各自读到 attempts 还没到上限、
// 然后一起完成比较——「一枚码最多猜 5 次」被放大成 N 次，而所有串行测试依然全绿。
//
// 这里的做法是把预留强制为失败：正确顺序下，没有任何请求能走到比较那一步，
// 于是**即便提交正确的码也必须被拒**；若有人把预留挪到比较之后（或忽略它的返回值），
// 正确的码就会被接受，测试立刻失败。
//
// 反证方式：让 ReserveAttempt 的返回值不被检查 → 本条失败。
func TestReservationIsAPreconditionForVerification(t *testing.T) {
	svc, sender, _ := newTestUserService(t)
	ctx := context.Background()

	const email = "ordering@example.com"
	if err := svc.RequestEmailCode(ctx, email); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	correct := sender.latest()

	stub := &reservingRepoStub{EmailVerificationRepository: svc.codes, refuse: true}
	svc.codes = stub

	if _, err := svc.Register(ctx, email, correct, "ordering-user", "nick", "GoodPass123"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("with reservations refused the correct code was still accepted (err=%v); "+
			"the attempt must be reserved BEFORE the hash comparison", err)
	}
	if stub.calls == 0 {
		t.Fatal("ReserveAttempt was never called; the reservation is not on the verification path")
	}

	// 对照：同一枚码在预留放行时正常工作，说明上面的失败确实来自预留被拒，
	// 而不是别的什么原因（例如码本身已失效）。
	svc.codes = stub.EmailVerificationRepository
	if _, err := svc.Register(ctx, email, correct, "ordering-user", "nick", "GoodPass123"); err != nil {
		t.Fatalf("the control run failed, so the test above proves nothing: %v", err)
	}
}

// TestVerificationCodeAttemptCapHoldsUnderConcurrency 钉住上限在**并发**下同样成立。
//
// 断言选的是数据库里的 attempts 计数，而不是返回值：所有猜测本来就是错的，
// 返回值恒为 ErrInvalidVerificationCode，看不出差别；计数才反映「一共有多少次
// 猜测被真正放行」，那正是这枚码的剩余安全性。
//
// 反证方式：去掉 ReserveAttempt 的 `AND attempts < ?` 条件 → attempts 会涨到并发数。
func TestVerificationCodeAttemptCapHoldsUnderConcurrency(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	const email = "concurrent-brute@example.com"
	if err := svc.RequestEmailCode(ctx, email); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	if code := sender.latest(); code == "00000000" {
		t.Skip("the generator produced the placeholder code; nothing to assert")
	}

	const parallel = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 尽量让所有 goroutine 同时冲进 consumeCode
			if _, err := svc.Register(ctx, email, "00000000", "concurrent-user", "nick", "GoodPass123"); !errors.Is(err, ErrInvalidVerificationCode) {
				t.Errorf("concurrent guess error = %v, want ErrInvalidVerificationCode", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	var attempts int
	if err := db.QueryRow(
		`SELECT attempts FROM email_verification_codes WHERE email = ?`, email,
	).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}

	if attempts > maxVerificationCodeAttempts {
		t.Fatalf("%d concurrent guesses were let through (attempts=%d, cap=%d): "+
			"the attempt cap does not hold under concurrency",
			parallel, attempts, maxVerificationCodeAttempts)
	}
	if attempts == 0 {
		t.Fatal("no attempt was recorded at all; the reservation path did not run")
	}

	// 上限用尽后，即便拿正确的码也必须被拒——否则上限只是延迟而非阻断。
	if _, err := svc.Register(ctx, email, sender.latest(), "concurrent-user", "nick", "GoodPass123"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("after exhausting the allowance the correct code was still accepted (err=%v)", err)
	}
}

func TestCodeCannotBeReused(t *testing.T) {
	svc, sender, _ := newTestUserService(t)
	ctx := context.Background()

	if err := svc.RequestEmailCode(ctx, "a@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	code := sender.latest()

	if _, err := svc.Register(ctx, "a@example.com", code, "reuse-one", "nick", "GoodPass123"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if _, err := svc.Register(ctx, "b@example.com", code, "reuse-two", "nick", "GoodPass123"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("a consumed code was accepted for another address (err=%v)", err)
	}
}

func TestDuplicateRegistrationIsRejected(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	register(t, svc, sender, db, "a@example.com", "first-user", "GoodPass123", "first")

	advancePastEmailCodeInterval(t, db, "a@example.com")
	if err := svc.RequestEmailCode(ctx, "a@example.com"); err != nil {
		t.Fatalf("second RequestEmailCode() error = %v", err)
	}
	// Even with the *same* username: the real reason is the address, and saying
	// "username taken" would send the user off to fix the wrong field.
	_, err := svc.Register(ctx, "a@example.com", sender.latest(), "first-user", "second", "GoodPass456")
	if !errors.Is(err, ErrEmailAlreadyRegistered) {
		t.Fatalf("duplicate registration error = %v, want ErrEmailAlreadyRegistered", err)
	}
}

// TestRegistrationDoesNotRevealAccountExistence 断言注册端点在**没有有效验证码**
// 时不会泄露任何与既有账号有关的信息。
//
// 这个端点是全站唯一一个不需要登录、又能触碰账号命名空间的写入口。它的占用
// 检查（邮箱是否已注册、用户名是否被占）必须排在验证码校验**之后**：否则一个
// 乱填的码就足以区分「已注册 / 空闲」——攻击者可以逐个试出全站注册用户，
// 也能把站长占用的登录名试出来。
//
// 反证方式：把 Register 里 consumeCode 的调用挪到 FindByEmail 之后，
// 「已知邮箱」与「站长登录名」两个子用例会分别拿到 ErrEmailAlreadyRegistered
// 与 ErrUsernameTaken，测试即刻失败。
func TestRegistrationDoesNotRevealAccountExistence(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	// 站长的登录名占着一个用户名的名额，而它同样受「先验证码、后占用检查」这条
	// 顺序保护——所以这里必须真的有那一行，否则下面的子用例会因为名字空闲而
	// 注册成功，测试也就失去了它要断言的东西。
	seedAdministrator(t, db, "admin")

	register(t, svc, sender, db, "known@example.com", "known-user", "GoodPass123", "known")

	// 让两个地址各自持有一枚**有效的**码，再用一个错误的码去注册。这样走的是
	// 「码存在、比对失败」这条路径，而不是「根本没有码」那条更简单的路径。
	advancePastEmailCodeInterval(t, db, "known@example.com")
	if err := svc.RequestEmailCode(ctx, "known@example.com"); err != nil {
		t.Fatalf("RequestEmailCode(known) error = %v", err)
	}
	knownCode := sender.latest()
	if err := svc.RequestEmailCode(ctx, "unknown@example.com"); err != nil {
		t.Fatalf("RequestEmailCode(unknown) error = %v", err)
	}
	unknownCode := sender.latest()

	wrong := "00000000"
	if wrong == knownCode || wrong == unknownCode {
		wrong = "11111111"
	}

	// 已注册的邮箱 + 错误的码：必须是「码无效」，而不是「邮箱已注册」。
	if _, err := svc.Register(ctx, "known@example.com", wrong, "free-name", "nick", "GoodPass456"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("probing a known address with a wrong code error = %v, want ErrInvalidVerificationCode", err)
	}

	// 空闲邮箱 + 站长占用的登录名 + 错误的码：同样必须是「码无效」。
	// 这里若返回 ErrUsernameTaken，就等于不用任何验证码就能试出站长的登录名。
	if _, err := svc.Register(ctx, "unknown@example.com", wrong, "admin", "nick", "GoodPass456"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("probing the administrator login name with a wrong code error = %v, want ErrInvalidVerificationCode", err)
	}

	// 探测不该产生任何副作用。
	if _, err := svc.users.FindByEmail(ctx, "unknown@example.com"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("a probe created an account: %v", err)
	}

	// 对照组：一旦调用方**确实**持有该邮箱的有效验证码，如实相告才是正确行为
	// ——此刻它已经证明了自己控制这个邮箱，而占用信息对它是可用的。
	if _, err := svc.Register(ctx, "known@example.com", knownCode, "free-name", "nick", "GoodPass456"); !errors.Is(err, ErrEmailAlreadyRegistered) {
		t.Fatalf("registering a known address with its own valid code error = %v, want ErrEmailAlreadyRegistered", err)
	}
	if _, err := svc.Register(ctx, "unknown@example.com", unknownCode, "admin", "nick", "GoodPass456"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("registering with the administrator login name and a valid code error = %v, want ErrUsernameTaken", err)
	}

	// 最后：上面那次被拒的尝试已经把码消费掉了，所以换名字要继续就得重新申请。
	// 这正是「校验即消费」的既定代价（见 TestRejectedRegistrationConsumesTheCode），
	// 换来的是完全没有「校验→检查→消费」之间的窗口。
	//
	// 这个地址在本用例里已经要过一枚码，所以先推过短间隔节流。
	advancePastEmailCodeInterval(t, db, "unknown@example.com")
	if err := svc.RequestEmailCode(ctx, "unknown@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() after the rejection error = %v", err)
	}
	user, err := svc.Register(ctx, "unknown@example.com", sender.latest(), "free-name-2", "nick", "GoodPass456")
	if err != nil {
		t.Fatalf("Register() after choosing a free username error = %v", err)
	}
	if user.Username != "free-name-2" {
		t.Fatalf("username = %q, want free-name-2", user.Username)
	}
}

// TestRejectedRegistrationConsumesTheCode 钉住「校验即消费」这条**有意选择**的行为。
//
// 它看起来像是浪费（用户只是因为重名被拒，却得重新收一封邮件），但它买到的是两件事：
//  1. 完全没有「校验 → 做检查 → 消费」之间的窗口，同一枚码不可能授权两次操作；
//  2. 注册端点不会在验证码通过之前泄露任何账号占用信息。
//
// 所以这条测试的作用是**防止有人出于好意把消费挪到检查之后**：那一步会同时把上面
// 两件事一起拿掉，而所有功能测试仍然会是绿的。反证方式：把 Register 里的
// consumeCode 调用移到 FindByEmail/ensureUsernameAvailable 之后，
// 第二个断言会看到 ErrEmailAlreadyRegistered 或成功，测试立刻失败。
func TestRejectedRegistrationConsumesTheCode(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	register(t, svc, sender, db, "owner@example.com", "taken-name", "GoodPass123", "first")

	// 用一个已被占用的用户名注册，拿到的是一枚全新的有效码。
	if err := svc.RequestEmailCode(ctx, "newcomer@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	rejected := sender.latest()
	if _, err := svc.Register(ctx, "newcomer@example.com", rejected, "taken-name", "nick", "GoodPass456"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("Register() with a taken username error = %v, want ErrUsernameTaken", err)
	}

	// 关键断言：那枚码已经不能再用——即便这次换了一个完全可用的用户名。
	if _, err := svc.Register(ctx, "newcomer@example.com", rejected, "free-name", "nick", "GoodPass456"); !errors.Is(err, ErrInvalidVerificationCode) {
		t.Fatalf("the code survived a rejected registration (err=%v); "+
			"the code must be consumed before any occupancy check runs", err)
	}

	// 重新申请之后一切照常（先推过短间隔节流）。
	advancePastEmailCodeInterval(t, db, "newcomer@example.com")
	if err := svc.RequestEmailCode(ctx, "newcomer@example.com"); err != nil {
		t.Fatalf("second RequestEmailCode() error = %v", err)
	}
	user, err := svc.Register(ctx, "newcomer@example.com", sender.latest(), "free-name", "nick", "GoodPass456")
	if err != nil {
		t.Fatalf("Register() with a fresh code error = %v", err)
	}
	if user.Username != "free-name" {
		t.Fatalf("username = %q, want free-name", user.Username)
	}
}

// TestLoginDoesNotRevealAccountExistence asserts that an unknown identifier and
// a wrong password are indistinguishable to the caller.
func TestLoginDoesNotRevealAccountExistence(t *testing.T) {
	svc, sender, db := newTestUserService(t)

	register(t, svc, sender, db, "a@example.com", "exists-user", "GoodPass123", "nick")

	if _, err := svc.LoginWithUsername(context.Background(), "exists-user", "WrongPass123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.LoginWithUsername(context.Background(), "nobody-here", "GoodPass123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown account error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.LoginWithUsername(context.Background(), "nobody@example.com", "GoodPass123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown address error = %v, want ErrInvalidCredentials", err)
	}
}

// TestDummyPasswordHashIsUsable guards the value the timing mitigation rests on.
//
// bcrypt rejects malformed hashes, and anything shorter than 59 bytes, before
// running a single key-derivation round. A corrupted dummyPasswordHash would
// therefore turn the unknown-account path into a microsecond fast path while
// every other test in this file stayed green.
func TestDummyPasswordHashIsUsable(t *testing.T) {
	if got := len(dummyPasswordHash); got != 60 {
		t.Fatalf("len(dummyPasswordHash) = %d, want 60", got)
	}

	cost, err := bcrypt.Cost([]byte(dummyPasswordHash))
	if err != nil {
		t.Fatalf("bcrypt.Cost(dummyPasswordHash) error = %v; the value is not a usable bcrypt hash", err)
	}
	if cost != bcrypt.DefaultCost {
		t.Fatalf("dummyPasswordHash cost = %d, want %d so it matches hashPassword()", cost, bcrypt.DefaultCost)
	}
}

// TestLoginTimingIsEqualized asserts that the login paths which must be
// indistinguishable really do spend comparable time.
//
// Each path is compared against a genuine verification measured in the same
// run, so the assertions do not depend on absolute machine speed. The ratio is
// deliberately loose: the failure being guarded against is a return in
// microseconds versus tens of milliseconds, not a few percent of jitter.
func TestLoginTimingIsEqualized(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	register(t, svc, sender, db, "timed@example.com", "timed-user", "GoodPass123", "nick")

	// A commenter account that exists but never set a password. It also predates
	// the username column, so it doubles as the "not yet backfilled" shape.
	if _, err := db.Exec(
		`INSERT INTO users (email, nickname, password_hash, created_at, updated_at) VALUES ('nopass@example.com', '旧用户', '', 1, 1)`,
	); err != nil {
		t.Fatalf("seed passwordless user: %v", err)
	}

	elapsed := func(identifier string) time.Duration {
		t.Helper()
		start := time.Now()
		_, err := svc.LoginWithUsername(ctx, identifier, "WrongPass123")
		d := time.Since(start)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("LoginWithUsername(%q) error = %v, want ErrInvalidCredentials", identifier, err)
		}
		return d
	}

	// Warm up so the first measured call does not pay one-off costs.
	elapsed("timed-user")

	baseline := elapsed("timed-user")             // wrong password: real bcrypt work
	unknown := elapsed("nobody-here")             // identifier is not registered
	unknownEmail := elapsed("nobody@example.com") // address-shaped, also unknown
	noPassword := elapsed("nopass@example.com")   // registered but passwordless

	const minRatio = 0.25
	floor := time.Duration(float64(baseline) * minRatio)
	for name, measured := range map[string]time.Duration{
		"unknown identifier": unknown,
		"unknown address":    unknownEmail,
		"passwordless":       noPassword,
	} {
		if measured < floor {
			t.Errorf("%s login took %v, want >= %v (25%% of a real check at %v); "+
				"the timing mitigation is not performing real bcrypt work", name, measured, floor, baseline)
		}
	}
}

// TestLegacyCommenterCanSetAPassword documents the upgrade path for accounts
// created before passwords — and before usernames — existed.
func TestLegacyCommenterCanSetAPassword(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	if _, err := db.Exec(
		`INSERT INTO users (email, nickname, password_hash, created_at, updated_at) VALUES ('old@example.com', '旧用户', '', 1, 1)`,
	); err != nil {
		t.Fatalf("seed legacy user: %v", err)
	}

	// Without a password the account cannot sign in yet.
	if _, err := svc.LoginWithUsername(ctx, "old@example.com", "GoodPass123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("passwordless login error = %v, want ErrInvalidCredentials", err)
	}

	if err := svc.RequestEmailCode(ctx, "old@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}
	user, err := svc.Register(ctx, "old@example.com", sender.latest(), "upgraded-old", "新昵称", "GoodPass123")
	if err != nil {
		t.Fatalf("upgrade Register() error = %v", err)
	}
	if user.Nickname != "新昵称" {
		t.Fatalf("nickname = %q, want 新昵称", user.Nickname)
	}
	if user.Username != "upgraded-old" {
		t.Fatalf("username = %q, want upgraded-old", user.Username)
	}
	if _, err := svc.LoginWithUsername(ctx, "upgraded-old", "GoodPass123"); err != nil {
		t.Fatalf("login after upgrade failed: %v", err)
	}
}

func TestResetPassword(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	register(t, svc, sender, db, "a@example.com", "reset-user", "GoodPass123", "nick")

	advancePastEmailCodeInterval(t, db, "a@example.com")
	if err := svc.RequestEmailCode(ctx, "a@example.com"); err != nil {
		t.Fatalf("reset RequestEmailCode() error = %v", err)
	}
	if _, err := svc.ResetPassword(ctx, "a@example.com", sender.latest(), "BrandNew456"); err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}

	if _, err := svc.LoginWithUsername(ctx, "reset-user", "GoodPass123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("the old password still works after a reset")
	}
	if _, err := svc.LoginWithUsername(ctx, "reset-user", "BrandNew456"); err != nil {
		t.Fatalf("the new password was rejected: %v", err)
	}

	// An unknown address is only reachable after passing the code check, so this
	// 404 cannot be used to probe whether an address is registered: an attacker
	// who does not control the mailbox never gets a usable code.
	if err := svc.RequestEmailCode(ctx, "nobody@example.com"); err != nil {
		t.Fatalf("RequestEmailCode(nobody) error = %v", err)
	}
	if _, err := svc.ResetPassword(ctx, "nobody@example.com", sender.latest(), "BrandNew456"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("reset for an unknown address error = %v, want ErrAccountNotFound", err)
	}
}

// TestChangePassword 覆盖访客账号的自助改密。
//
// 返回的 *model.User 是这次重构新增的：handler 需要拿 IsAdmin 来决定改密后按哪种
// 角色重新签发会话（见 handler.AuthHandler.reissueSession）。所以这里除了错误之外
// 还要确认返回的账号是对的——返回 nil 而 error 为 nil 会让 handler 直接 panic。
func TestChangePassword(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	user := register(t, svc, sender, db, "a@example.com", "change-user", "GoodPass123", "nick")

	if _, err := svc.ChangePassword(ctx, user.ID, "WrongPass123", "Another456"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong current password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.ChangePassword(ctx, user.ID, "GoodPass123", "weak"); err == nil {
		t.Fatal("a weak new password was accepted")
	}
	updated, err := svc.ChangePassword(ctx, user.ID, "GoodPass123", "Another456")
	if err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if updated == nil {
		t.Fatal("ChangePassword() returned no account, but the caller needs it to reissue the session")
	}
	if updated.ID != user.ID || updated.IsAdmin {
		t.Fatalf("ChangePassword() returned the wrong account: %+v", updated)
	}
	if _, err := svc.LoginWithUsername(ctx, "change-user", "Another456"); err != nil {
		t.Fatalf("login with the new password failed: %v", err)
	}
}

func TestNicknameAndProfileUpdates(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	user := register(t, svc, sender, db, "a@example.com", "profile-user", "GoodPass123", "nick")

	if _, err := svc.UpdateOwnNickname(ctx, user.ID, "  新名  "); err != nil {
		t.Fatalf("UpdateOwnNickname() error = %v", err)
	}
	updated, err := svc.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if updated.Nickname != "新名" {
		t.Fatalf("nickname = %q, want 新名", updated.Nickname)
	}
	if updated.Email != "a@example.com" {
		t.Fatal("updating the nickname must not touch the email")
	}
	if updated.Username != "profile-user" {
		t.Fatal("updating the nickname must not touch the username")
	}

	if _, err := svc.UpdateOwnNickname(ctx, user.ID, ""); err == nil {
		t.Fatal("an empty nickname was accepted")
	}

	// An administrator may reassign all three: the login identifier, the display
	// name and the address.
	if _, err := svc.AdminUpdateUser(ctx, user.ID, "admin-renamed", "管理员改的", "moved@example.com"); err != nil {
		t.Fatalf("AdminUpdateUser() error = %v", err)
	}
	moved, err := svc.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if moved.Email != "moved@example.com" || moved.Nickname != "管理员改的" || moved.Username != "admin-renamed" {
		t.Fatalf("unexpected profile after admin update: %+v", moved)
	}

	// A taken address must be reported, not silently duplicated.
	register(t, svc, sender, db, "other@example.com", "other-user", "GoodPass123", "other")

	if _, err := svc.AdminUpdateUser(ctx, user.ID, "admin-renamed", "x", "other@example.com"); !errors.Is(err, ErrEmailAlreadyRegistered) {
		t.Fatalf("duplicate email error = %v, want ErrEmailAlreadyRegistered", err)
	}
	// Likewise for the login identifier, and the two must not be confused.
	if _, err := svc.AdminUpdateUser(ctx, user.ID, "other-user", "x", "free@example.com"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate username error = %v, want ErrUsernameTaken", err)
	}
}

func TestListUsers(t *testing.T) {
	svc, sender, db := newTestUserService(t)
	ctx := context.Background()

	for _, spec := range []struct{ email, username string }{
		{"a@example.com", "list-user-a"},
		{"b@example.com", "list-user-b"},
		{"c@example.com", "list-user-c"},
	} {
		register(t, svc, sender, db, spec.email, spec.username, "GoodPass123", "nick")
	}

	users, total, err := svc.ListUsers(ctx, model.Pagination{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(users) != 2 {
		t.Fatalf("page returned %d users, want 2", len(users))
	}
}

// TestVerificationEmailIsWrittenInChinese 钉住验证码邮件的中文文案。
//
// 站点的读者与收件人都是中文用户，邮件曾经是英文的（"Your verification code
// is ..."），与站点语言不一致。这条测试同时守住三件事：主题与正文是中文、
// 正文里仍然带着验证码本身（SendVerification 依赖它从留痕表里抹掉敏感内容，
// redactSecret 曾因此被破坏过）、有效期分钟数与 verificationCodeTTL 一致。
// 反证方式：把 user_service.go 的邮件文案改回英文，本条立即失败。
func TestVerificationEmailIsWrittenInChinese(t *testing.T) {
	svc, sender, _ := newTestUserService(t)

	if err := svc.RequestEmailCode(context.Background(), "reader@example.com"); err != nil {
		t.Fatalf("RequestEmailCode() error = %v", err)
	}

	body := sender.lastBody()
	if !strings.Contains(body, "您的验证码是") {
		t.Fatalf("the verification email body is not the Chinese copy: %q", body)
	}
	if !strings.Contains(body, "分钟内有效") {
		t.Fatalf("the verification email body does not state the TTL: %q", body)
	}

	match := codePattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("the Chinese body no longer carries the code itself: %q", body)
	}

	// 正文声明的时间窗必须与服务端的 TTL 同源，而不是一句写死的孤立文案。
	want := int(verificationCodeTTL.Minutes())
	if !strings.Contains(body, strconv.Itoa(want)+" 分钟内有效") {
		t.Fatalf("the body does not state the %d-minute TTL: %q", want, body)
	}
}
