package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	netmail "net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"blog-engine/internal/config"
	"blog-engine/internal/logging"
	"blog-engine/internal/model"
)

// verificationCodeTTL 是邮件验证码的有效期。
const verificationCodeTTL = 10 * time.Minute

// emailCodesPerHourPerAddress 是同一收件地址每小时可签发验证码的上限。
//
// 它与 middleware 里按 IP 的额度（默认 10/h）刻意取同一个量级，但管的是另一件事：
// 按 IP 的额度限制的是「一个来源能发多少」，这个限制的是「一个收件箱会被打扰多少」。
// 分布式请求下前者形同虚设，而受害者的邮箱只有一个。
//
// 10 这个值足够真实用户重试（验证码 10 分钟有效，正常不会要第二枚），
// 又把「无限刷」压成「每小时最多十封」。若仍然被滥用，下一步才是图形验证码——
// 那会引入第三方依赖并拖慢注册，暂时不值得。
const emailCodesPerHourPerAddress = 10

// emailCodeMinInterval 是同一收件地址两次签发验证码之间的最短间隔。
//
// 它挡的是「连点」与「脚本高频重发」，与 emailCodesPerHourPerAddress 是两条
// 不同粒度的闸：小时的额度允许 10 枚（够真实用户重试），但**不允许 10 枚在一秒内
// 打完**——那对收件箱是骚扰，对发信通道是浪费，对攻击者则是把「每小时 10 次」
// 压缩成一次突发。
//
// 60 秒的选择依据：验证码本身 10 分钟有效，正常用户几乎不需要第二枚；而"点了没收到、
// 想再要一封"是唯一的高频场景，等一分钟是合理的。邮件投递本身也常有几十秒延迟，
// 间隔太短会让用户拿到两枚码而分不清哪枚有效（虽然旧码会被作废，但用户手上
// 可能正拿着刚抄下来的那一枚）。
//
// 这是**服务端**的判定，前端那个倒计时只是体验优化——绕过前端直接打接口时，
// 唯一还能拦住连发的就是这里。
const emailCodeMinInterval = 60 * time.Second

// maxVerificationCodeAttempts 是同一枚验证码允许被猜错的次数，超过即作废。
//
// 5 这个值留足了手滑的余地（正常用户最多输错一两次），同时把「在十分钟有效期
// 内穷举验证码」从「取决于攻击者有多少 IP」变成「不可能」：任何一枚码最多
// 只能被验证 5 次，猜不中就得让受害者重新签发一枚。
//
// ⚠️ 这个上限由 ReserveAttempt 在**比较之前**原子地执行（见 consumeCode）。
// 曾经的写法是「先比较、失败后再递增计数」，那在串行下完全正确，并发下却会失效：
// N 个请求各自读到 attempts 还没到上限，然后一起完成比较，上限被放大成 N
// （实测 200 并发 = 200 次猜测）。改动那一处顺序前请先读 consumeCode 的说明。
const maxVerificationCodeAttempts = 5

// UserService 负责**访客**账号体系：注册、密码登录、密码重置，以及账号自助资料修改。
//
// 它只处理访客这一类账号；站长那一行（is_admin = 1）由 AdminAuth 管理。
// 但两者**共用一张表**，因此「用户名全站唯一」只需要 users 表上的一个部分唯一索引
// （idx_users_username）就够了——不再需要「站长的登录名存在别处、要在这里补一次
// 预留判定」那套跨命名空间的推理。这正是把管理员凭据并进 users 表的主要收益。
//
// 本文件里还有一处刻意的「反账号枚举」设计，改动前务必读懂，否则很容易被当成
// 冗余代码删掉：LoginWithUsername 对「标识不存在」「账号从未设过密码」「密码错误」
// 返回完全相同的错误，并且前两种情况下仍然执行一次 bcrypt 比较，使耗时与真实
// 校验对齐。配合它使用的 dummyPasswordHash 另有一条硬约束，见那个常量的说明。
type UserService struct {
	users  model.UserRepository
	codes  model.EmailVerificationRepository
	sender EmailSender

	// emailCodeIntervalOverride 覆盖 emailCodeMinInterval，**只供测试使用**。
	//
	// 为什么需要这个口子：短间隔节流按收件地址生效，而几乎每条账号流程
	// （注册、重名重试、重置密码）都需要「为同一个地址连发两枚码」。真实间隔是
	// 60 秒，测试里逐个 sleep 会让 service 包多出十几分钟；而把间隔做成可注入的，
	// 就能让这些流程保持**真实调用路径**的同时把等待压到零。
	//
	// 0 表示「用常量值」（见 emailCodeInterval）。它不来自配置文件，也不该有
	// 任何生产写入路径——一旦它变成可配置项，站长就能把防连点闸门调到 1 秒，
	// 而那正是这个功能要挡的东西。
	emailCodeIntervalOverride time.Duration
}

func NewUserService(
	users model.UserRepository,
	codes model.EmailVerificationRepository,
	sender EmailSender,
) *UserService {
	return &UserService{users: users, codes: codes, sender: sender}
}

// emailCodeInterval 返回实际生效的短间隔节流窗口。
//
// 读的是字段而不是常量，使测试能在不改动任何调用点的前提下缩短窗口。
// 集中的取值方法让「0 = 用默认值」这条规则只有一处。
func (s *UserService) emailCodeInterval() time.Duration {
	if s.emailCodeIntervalOverride > 0 {
		return s.emailCodeIntervalOverride
	}
	return emailCodeMinInterval
}

// RequestEmailCode 生成并发送一枚验证码。
//
// 注册与重置密码共用同一枚码：两条流程需要的证明本来就是同一件事——收件人
// 必须已经控制这个邮箱地址。签发前先作废该邮箱下所有仍然有效的旧码，
// 否则每请求一次就会多出一枚同时有效的码，把攻击者可用的猜测次数成倍放大。
//
// 这个接口没有鉴权（注册本来就不可能有会话），所以它同时是本站唯一一个
// 「让陌生人的请求触发一封发往任意地址的邮件」的入口。按 IP 的限流是第一道闸，
// 但换 IP 就能重新拿到额度；因此这里再按**收件地址**计一次数（见
// emailCodesPerHourPerAddress），挡住「用一台机器群发去刷爆某一个收件箱」。
func (s *UserService) RequestEmailCode(ctx context.Context, email string) error {
	email, err := normalizeAndValidateEmail(email)
	if err != nil {
		return err
	}

	// 计数在作废旧码之前：被限额拒绝的请求不应该顺手把用户手上那枚仍有效的码作废。
	now := time.Now()
	issued, err := s.codes.CountCreatedSince(ctx, email, now.Add(-time.Hour).Unix())
	if err != nil {
		logging.Errorf("[user] count verification codes failed email=%s: %v", logging.RedactEmail(email), err)
		return err
	}
	if issued >= emailCodesPerHourPerAddress {
		logging.Warnf("[user] per-address verification code limit reached email=%s issued=%d", logging.RedactEmail(email), issued)
		return ErrEmailCodeAddressLimit
	}

	// 短间隔节流。同样放在作废之前——被拒的请求不该动用户手上那枚仍然有效的码。
	//
	// 这里必须 fail closed：查不出上次签发时间就**拒绝**，而不是放行。把查询失败
	// 当成「没发过」，会让一次数据库故障静默地把 60 秒闸门整个摘掉。
	//
	// 判定分两步，缺一不可：
	//   - `last > now`（时钟被往回拨）直接放行。曾经只用「now-last < interval」一个
	//     判据，而负数是「小于 interval」的，于是回拨期间用户被**永久**锁在节流里：
	//     每过一秒 remaining 反而变大。注释当时就写着「差值为负，直接放行」，
	//     代码却没有那一步——回拨时宁可宽松一次，也不要让人再也发不出验证码。
	//   - 其余情况用「now-last < interval」。若写成「last > now-interval」，在回拨
	//     时同样会把人锁死，这正是上面那条要防的。
	last, ok, err := s.codes.LastCreatedAt(ctx, email)
	if err != nil {
		logging.Errorf("[user] read last verification code time failed email=%s: %v", logging.RedactEmail(email), err)
		return err
	}
	interval := int64(s.emailCodeInterval().Seconds())
	if ok && last <= now.Unix() && now.Unix()-last < interval {
		remaining := interval - (now.Unix() - last)
		logging.Warnf("[user] verification code requested too soon email=%s remaining=%ds", logging.RedactEmail(email), remaining)
		return ErrEmailCodeTooFrequent
	}

	code, err := generateVerificationCode()
	if err != nil {
		logging.Errorf("[user] generate verification code failed: %v", err)
		return err
	}

	if err := s.codes.InvalidateActiveForEmail(ctx, email, now.Unix()); err != nil {
		logging.Errorf("[user] invalidate previous codes failed email=%s: %v", logging.RedactEmail(email), err)
		return err
	}

	record := &model.EmailVerificationCode{
		Email:     email,
		CodeHash:  hashVerificationCode(code),
		ExpiresAt: now.Add(verificationCodeTTL).Unix(),
		CreatedAt: now.Unix(),
	}
	if err := s.codes.Create(ctx, record); err != nil {
		logging.Errorf("[user] store verification code failed email=%s: %v", logging.RedactEmail(email), err)
		return err
	}

	// 走 SendVerification 而不是 Send：正文里的验证码不会进入邮件留痕表。
	// 用 Send 的话，任何能读到 /api/admin/emails/:id 的人（或任何拿到
	// 数据库/备份的人）都能取走这枚码去重置别人的密码。
	//
	// 正文是中文：站点的读者与收件人都是中文用户。验证码仍然原样出现在
	// 正文中——SendVerification 靠它从留痕表里抹掉敏感内容（redactSecret），
	// 正文不含验证码会让留痕失效。
	body := fmt.Sprintf("您的验证码是 %s，%d 分钟内有效。若非您本人操作，请忽略本邮件。", code, int(verificationCodeTTL.Minutes()))
	if err := s.sender.SendVerification(email, "您的验证码", body, code); err != nil {
		// 发送失败就必须让这枚码**当场失效**，不能留在库里等它自己过期。
		//
		// 否则会出现一种状态不一致：库里有一枚仍然有效（TTL 10 分钟）的验证码，
		// 而唯一有权使用它的收件人从未收到过它。合法用户拿不到，任何读到库的人
		// （数据库、备份、WAL；Windows 上还有 ACL 问题，见部署说明）却可以拿去
		// 重置别人的密码——也就是说，一次发信故障会静默地把「验证码」变成
		// 「只对攻击者可见的凭据」。
		//
		// 作废而不是删除：作废复用 InvalidateActiveForEmail 这条既有路径，
		// 记录仍会被 PruneExpired 正常清掉，不引入新的仓储方法。
		if invalidateErr := s.codes.InvalidateActiveForEmail(ctx, email, time.Now().Unix()); invalidateErr != nil {
			logging.Errorf("[user] invalidating an unsent verification code failed email=%s: %v", logging.RedactEmail(email), invalidateErr)
		}
		logging.Errorf("[user] send verification code failed email=%s: %v", logging.RedactEmail(email), err)
		return err
	}

	logging.Infof("[user] verification code sent email=%s", logging.RedactEmail(email))
	return nil
}

// Register 消费验证码后创建账号。
//
// 五个参数里 email 与 username 是**两件事**，不要合并：
//   - email 是联系方式，也是「确实是本人」的证明来源（验证码发到这里）；
//   - username 是登录标识，全站唯一，注册之后用户可以改但必须保持唯一。
//
// 分两条分支：邮箱从未出现过 → 新建账号；邮箱已存在但该账号从未设置过密码
// （历史遗留的「评论者」账号，见 model.User.PasswordHash 的说明）→ 就地升级，
// 补上用户名、密码与昵称。若该账号已经有密码，则返回 ErrEmailAlreadyRegistered。
//
// 顺序上有三处刻意的安排，改动时请一并保留：
//
//   - **验证码是整段流程的第一道闸，而且校验即消费**（consumeCode）。注册端点无需
//     登录即可调用；若把「邮箱/用户名是否已被占用」的检查排在验证码之前，它就变成
//     一个**不需要验证码的账号枚举预言机**：409 表示已注册、400 表示未注册，攻击者
//     可以逐个试出任意邮箱是否在本站注册过。必须先证明调用方控制这个邮箱，之后才
//     允许透露任何与既有账号有关的信息。
//   - **被拒绝的注册同样消费掉这枚码**，这是有意为之的取舍，不是疏漏。校验与消费
//     写在一次调用里，就不存在「先校验、再做若干检查、最后才消费」之间的那个窗口：
//     同一枚码不可能授权两次操作，也不必去推理并发下的中间状态。代价是用户因为
//     重名或重复注册而失败时需要重新申请一枚码（按地址限流是 10 封/小时，够用）。
//     请不要为了让被拒的用户少收一封邮件而把消费挪到检查之后——那会同时把枚举
//     预言机和那个并发窗口一起带回来。TestRejectedRegistrationConsumesTheCode
//     钉住了这条行为。
//   - **邮箱检查排在用户名检查之前**。同一个邮箱带着同一个用户名再次注册时，
//     真正的原因是「这个邮箱已经注册过了」；先报「用户名被占用」会把用户
//     引向一个错误的修法（换个用户名重试，然后再次失败）。
func (s *UserService) Register(ctx context.Context, email, code, username, nickname, password string) (*model.User, error) {
	email, err := normalizeAndValidateEmail(email)
	if err != nil {
		return nil, err
	}
	username, err = normalizeAndValidateUsername(username)
	if err != nil {
		return nil, err
	}
	nickname, err = normalizeAndValidateNickname(nickname)
	if err != nil {
		return nil, err
	}
	if err := config.ValidatePasswordStrength(password); err != nil {
		return nil, invalidf("%s", err.Error())
	}

	// 一道闸：校验并消费这枚验证码。通过之前不碰任何既有账号的状态；
	// 通过之后无论下面的检查是否拒绝，这枚码都已经不能再用。
	if err := s.consumeCode(ctx, email, code); err != nil {
		return nil, err
	}

	now := time.Now()
	existing, err := s.users.FindByEmail(ctx, email)
	switch {
	case err != nil && !errors.Is(err, model.ErrUserNotFound):
		return nil, err

	case err == nil && (existing.HasPassword() || existing.IsAdmin):
		// 走到这里说明调用方已经通过邮箱验证，如实相告是合适的。
		//
		// IsAdmin 是**纵深防御**：管理员行的 email 是空串哨兵，正常永远匹配不到
		// 任何注册请求；但万一它被写脏，也绝不能落进下面的「就地升级」分支——
		// 那会把站长账号改写成别人指定的用户名、昵称与密码。
		return nil, ErrEmailAlreadyRegistered

	case err == nil:
		// Legacy commenter account without a password: it is upgraded in place
		// instead of colliding with its own address.
		if err := s.ensureUsernameAvailable(ctx, username); err != nil {
			return nil, err
		}
		// 密码已经过 ValidatePasswordStrength（≤72 字节），bcrypt 不会失败；
		// hashPassword 的错误分支只保留给导出给 CLI 的 HashPasswordForStorage。
		hash, _ := hashPassword(password)
		if err := s.users.UpdatePassword(ctx, existing.ID, hash, now.Unix()); err != nil {
			return nil, err
		}
		if err := s.users.UpdateUsername(ctx, existing.ID, username, now.Unix()); err != nil {
			return nil, mapUsernameError(err)
		}
		if err := s.users.UpdateNickname(ctx, existing.ID, nickname, now.Unix()); err != nil {
			return nil, err
		}
		existing.PasswordHash = hash
		existing.Username = username
		existing.Nickname = nickname
		existing.UpdatedAt = now.Unix()
		logging.Infof("[user] registered (upgraded existing account) user_id=%d", existing.ID)
		return existing, nil

	default:
		if err := s.ensureUsernameAvailable(ctx, username); err != nil {
			return nil, err
		}
		// 同上：输入已通过强度校验，hashPassword 不会失败。
		hash, _ := hashPassword(password)
		created := &model.User{
			Email:        email,
			Username:     username,
			Nickname:     nickname,
			PasswordHash: hash,
			CreatedAt:    now.Unix(),
			UpdatedAt:    now.Unix(),
		}
		if err := s.users.Create(ctx, created); err != nil {
			return nil, mapUsernameError(err)
		}
		logging.Infof("[user] registered user_id=%d", created.ID)
		return created, nil
	}
}

// dummyPasswordHash 是一个随机丢弃密码的、格式合法的 bcrypt 哈希。
// LoginWithPassword 在那些「必须与真实校验无法区分」的分支上拿它来比较。
//
// 硬约束：它必须始终是 bcrypt.DefaultCost 下的合法哈希。bcrypt 对格式非法的输入、
// 以及任何短于 59 字节的输入，会在做完一轮密钥派生之前就返回——也就是说，
// 一旦这个常量被改坏，那层耗时对齐会退化成几微秒的短路返回而静默失效，
// 但代码读起来仍然像是正确的。TestDummyPasswordHashIsUsable 守护这条不变量。
const dummyPasswordHash = "$2a$10$AfQeX1gjl448zVrqV29jW.bMN8HjbLVIJdrSGsa7H39bOO.pm7da."

// LoginWithUsername 校验登录标识与密码。
//
// identifier 通常就是 username。**也接受邮箱作为兼容入口**：username 这一列是
// 后加的，回填（见 database.backfillUsernames）虽然保证了每个账号都有用户名，
// 但老访客并不会立刻知道自己的那串字符是什么，用当初注册的邮箱登进来是最不
// 意外的路径。这不违反「昵称与用户名不得混用」——邮箱是另一条独立的、经过
// 验证的身份凭证，从不参与展示。
//
// 账号不存在、账号没有密码、密码错误——三种情况返回完全相同的错误，
// 使这个接口无法被用来探测「哪些用户名/邮箱已注册」。
func (s *UserService) LoginWithUsername(ctx context.Context, identifier, password string) (*model.User, error) {
	identifier = strings.TrimSpace(identifier)
	password = strings.TrimSpace(password)
	if identifier == "" || password == "" {
		return nil, ErrInvalidCredentials
	}

	user, err := s.users.FindByUsername(ctx, identifier)
	if err != nil && !errors.Is(err, model.ErrUserNotFound) {
		return nil, err
	}
	if errors.Is(err, model.ErrUserNotFound) && strings.ContainsRune(identifier, '@') {
		// Only fall back to the address when the input actually looks like one,
		// so the common path stays a single query.
		user, err = s.users.FindByEmail(ctx, normalizeEmail(identifier))
		if err != nil && !errors.Is(err, model.ErrUserNotFound) {
			return nil, err
		}
	}
	if err != nil || !user.HasPassword() {
		// Unknown identifier, or a commenter account that never set a password.
		// Both must cost about as much as a real verification: returning early
		// here would make them measurable faster than a wrong password.
		equalizeLoginTiming(password)
		return nil, ErrInvalidCredentials
	}

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		logging.Warnf("[user] login failed user_id=%d", user.ID)
		return nil, ErrInvalidCredentials
	}

	logging.Infof("[user] login succeeded user_id=%d", user.ID)
	return user, nil
}

// ResetPassword 消费验证码后设置新密码。
// 与 Register 不同：这里邮箱必须已存在，否则返回 ErrAccountNotFound。
//
// 顺序与 Register 一致，而且是**一步**：consumeCode 校验并消费同一枚码，之后才去
// 查账号。所以「这个邮箱在本站有没有账号」这条信息，只会在调用方已经证明控制该邮箱
// 之后才被透露；被拒绝的尝试同样会消耗掉那枚码。
//
// 管理员的账号**刻意不从这条路走**（见下面的 IsAdmin 守卫）。它的 email 平时是
// 空串哨兵，normalizeAndValidateEmail 会让请求在更早的地方就失败，所以正常情况下
// 根本到不了这里；这道判断是纵深防御——万一 email 被别的路径写脏，公开的找回密码
// 流程也不该成为接管站长账号的通道。
func (s *UserService) ResetPassword(ctx context.Context, email, code, password string) (*model.User, error) {
	email, err := normalizeAndValidateEmail(email)
	if err != nil {
		return nil, err
	}
	if err := config.ValidatePasswordStrength(password); err != nil {
		return nil, invalidf("%s", err.Error())
	}

	// 与 Register 同一道闸：校验即消费，之后才允许询问账号是否存在。
	if err := s.consumeCode(ctx, email, code); err != nil {
		return nil, err
	}

	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, model.ErrUserNotFound) {
			return nil, ErrAccountNotFound
		}
		return nil, err
	}
	if err := refuseAdminAccountForPublicFlow(user, "reset-password"); err != nil {
		return nil, err
	}

	// 输入已通过强度校验，hashPassword 不会失败。
	hash, _ := hashPassword(password)
	if err := s.users.UpdatePassword(ctx, user.ID, hash, time.Now().Unix()); err != nil {
		return nil, err
	}

	logging.Infof("[user] password reset user_id=%d", user.ID)
	user.PasswordHash = hash
	return user, nil
}

// refuseAdminAccountForPublicFlow 是公开（无需登录）账号流程的一道守卫：
// 目标行若是管理员，一律当成「没有这个账号」。
//
// 为什么返回 ErrAccountNotFound 而不是一个更精确的错误：文案必须与「邮箱未注册」
// 完全一致，否则这个守卫本身就成了「这个地址是站长的」的枚举预言机——而
// Register/ResetPassword 在 consumeCode 之上建立的反枚举语义会被它一笔勾销。
func refuseAdminAccountForPublicFlow(user *model.User, flow string) error {
	if user == nil || !user.IsAdmin {
		return nil
	}
	logging.Warnf("[user] refused to run the public %s flow against the administrator row user_id=%d", flow, user.ID)
	return ErrAccountNotFound
}

// ChangePassword 为已登录**访客账号**修改密码，必须先验证当前密码。
// 从未设过密码的账号会得到一条校验错误，引导其改走「重置密码」流程。
//
// 它返回更新后的账号，而不再只是 error：调用方（handler）需要拿 IsAdmin 来决定
// 改密后按哪种角色重新签发会话。管理员走的是 service.AdminAuth.ChangePassword，
// 那条路径同样返回 *model.User——于是 reissueSession 对两种身份是同一段代码，
// 也就不存在「管理员忘了走管理员分支」这个静默降级的入口。
func (s *UserService) ChangePassword(ctx context.Context, userID int64, currentPassword, newPassword string) (*model.User, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !user.HasPassword() {
		return nil, invalidf("this account has no password yet")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(strings.TrimSpace(currentPassword))) != nil {
		return nil, ErrInvalidCredentials
	}
	if err := config.ValidatePasswordStrength(newPassword); err != nil {
		return nil, invalidf("%s", err.Error())
	}

	// 输入已通过强度校验，hashPassword 不会失败。
	hash, _ := hashPassword(newPassword)
	if err := s.users.UpdatePassword(ctx, userID, hash, time.Now().Unix()); err != nil {
		return nil, err
	}

	logging.Infof("[user] password changed user_id=%d", userID)
	user.PasswordHash = hash
	return user, nil
}

// UpdateOwnNickname 让账号修改自己的**展示名**。它不碰登录标识：
// 本节里改昵称不会影响登录，这是把两个字段拆开之后最直接的收益。
func (s *UserService) UpdateOwnNickname(ctx context.Context, userID int64, nickname string) (*model.User, error) {
	nickname, err := normalizeAndValidateNickname(nickname)
	if err != nil {
		return nil, err
	}
	if err := s.users.UpdateNickname(ctx, userID, nickname, time.Now().Unix()); err != nil {
		return nil, err
	}

	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	logging.Infof("[user] nickname updated user_id=%d", userID)
	return user, nil
}

// UpdateOwnUsername 让账号修改自己的**登录标识**。
//
// 已经存在的会话不受影响：会话行存的是 user_id，改用户名不会把本人踢下线。
// 用户名冲突（不区分大小写）返回 ErrUsernameTaken。
//
// 这里**没有**「这个用户名是站长的」这一项检查：站长也是 users 表里的一行，
// 与访客共用 idx_users_username 这个部分唯一索引，所以撞名由数据库拦下。
// 旧版要为它单独补一次预留判定，只因为站长的名字存在 settings.json 里。
func (s *UserService) UpdateOwnUsername(ctx context.Context, userID int64, username string) (*model.User, error) {
	username, err := normalizeAndValidateUsername(username)
	if err != nil {
		return nil, err
	}

	// Check against everyone else before writing, so the common "that name is
	// taken" case produces a clear message instead of a constraint error.
	existing, err := s.users.FindByUsername(ctx, username)
	if err != nil && !errors.Is(err, model.ErrUserNotFound) {
		return nil, err
	}
	if err == nil && existing.ID != userID {
		return nil, ErrUsernameTaken
	}

	if err := s.users.UpdateUsername(ctx, userID, username, time.Now().Unix()); err != nil {
		return nil, mapUsernameError(err)
	}

	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	logging.Infof("[user] username updated user_id=%d", userID)
	return user, nil
}

// GetByID 按 id 读取账号。
func (s *UserService) GetByID(ctx context.Context, userID int64) (*model.User, error) {
	return s.users.GetByID(ctx, userID)
}

// ListUsers 是管理员使用的账号目录（分页）。
func (s *UserService) ListUsers(ctx context.Context, p model.Pagination) ([]model.User, int64, error) {
	return s.users.List(ctx, p)
}

// AdminUpdateUser 供管理员一次订正他人的登录标识、显示名与邮箱。
// 两个唯一约束分别映射为 ErrUsernameTaken 与 ErrEmailAlreadyRegistered——
// 操作员需要知道到底该改哪一项。
//
// 站长的登录名也在这同一个索引下，所以「改到站长的名字上」同样得到
// ErrUsernameTaken，不需要任何额外的判断。
func (s *UserService) AdminUpdateUser(ctx context.Context, userID int64, username, nickname, email string) (*model.User, error) {
	username, err := normalizeAndValidateUsername(username)
	if err != nil {
		return nil, err
	}
	nickname, err = normalizeAndValidateNickname(nickname)
	if err != nil {
		return nil, err
	}
	email, err = normalizeAndValidateEmail(email)
	if err != nil {
		return nil, err
	}

	if err := s.users.UpdateProfile(ctx, userID, username, nickname, email, time.Now().Unix()); err != nil {
		if errors.Is(err, model.ErrUsernameTaken) {
			return nil, ErrUsernameTaken
		}
		if errors.Is(err, model.ErrEmailTaken) {
			return nil, ErrEmailAlreadyRegistered
		}
		return nil, err
	}

	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	logging.Infof("[admin] updated user_id=%d", userID)
	return user, nil
}

// ensureUsernameAvailable 在注册前预检用户名是否已被占用。
// 它只是给用户一个及时提示；真正的唯一性由数据库的部分唯一索引保证，
// 两个并发注册仍然会在写库时被其中一个拦下（届时 mapUsernameError 兜底）。
//
// 这个索引同时覆盖站长的登录名——两者在同一张表、同一列上。
func (s *UserService) ensureUsernameAvailable(ctx context.Context, username string) error {
	_, err := s.users.FindByUsername(ctx, username)
	switch {
	case err == nil:
		return ErrUsernameTaken
	case errors.Is(err, model.ErrUserNotFound):
		return nil
	default:
		return err
	}
}

// mapUsernameError 把仓储层的用户名冲突统一翻译成 ErrUsernameTaken。
func mapUsernameError(err error) error {
	if errors.Is(err, model.ErrUsernameTaken) {
		return ErrUsernameTaken
	}
	return err
}

// consumeCode 校验一枚验证码并**立即消费它**——两件事在一次调用里完成，中间不插
// 任何其他工作。
//
// 这两步不可分割，是它存在的全部理由：只要允许「先校验、调用方稍后再消费」，就有了
// 一个窗口。窗口内两个并发请求会同时通过校验（验证码空间不大、有效期十分钟，这类争用
// 实测可复现），于是同一枚码授权了两次操作。把消费紧跟在比较之后，调用方就无从制造
// 这个窗口——Register 与 ResetPassword 都只调用它一次，且都在它之后才去查账号状态。
//
// 消费走 ClaimCode：一条带 `AND consumed_at IS NULL` 的原子 UPDATE，并检查受影响
// 行数为 0 即 fail closed。所以即便真的有并发，抢先用掉的那一次会拿到
// ErrVerificationCodeConsumed，而不是静默成功。
//
// 四个细节：
//   - **猜测机会在比较之前原子预留**（ReserveAttempt）。顺序反过来会让
//     「一枚码最多猜 maxVerificationCodeAttempts 次」在并发下失效：N 个请求
//     各自读到 attempts 还没到上限，然后一起完成比较，上限被放大成 N。
//     预留成功才允许比较，于是能走到比较那一步的请求最多只有 limit 个。
//   - 比较走 bcrypt（与密码同一套慢哈希），因此是恒定时间的；
//   - 数据库里从不存明文验证码，也不存「无盐 SHA-256」那种对短秘密等于明文的
//     形式——理由见 hashVerificationCode；
//   - 「预留失败」与「码不对」对外返回**同一个错误**，不给出可区分的信号。
func (s *UserService) consumeCode(ctx context.Context, email, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return invalidf("verification code is required")
	}

	now := time.Now()
	record, err := s.codes.FindLatestActiveByEmail(ctx, email, now.Unix())
	if err != nil {
		if errors.Is(err, model.ErrVerificationCodeNotFound) {
			return ErrInvalidVerificationCode
		}
		return err
	}

	// 先占坑，再比较。这一步是「最多猜 N 次」的执行点，不能挪到比较之后。
	reserved, err := s.codes.ReserveAttempt(ctx, record.ID, maxVerificationCodeAttempts)
	if err != nil {
		logging.Errorf("[user] reserve verification attempt failed email=%s: %v", logging.RedactEmail(email), err)
		return err
	}
	if !reserved {
		// 已被并发请求认领，或次数已用尽。对外与「码不对」同一口径：
		// 区分这两者会给攻击者一个廉价的进度指示。
		logging.Warnf("[user] verification attempt refused email=%s code_id=%d", logging.RedactEmail(email), record.ID)
		return ErrInvalidVerificationCode
	}

	if !verifyVerificationCode(record.CodeHash, code) {
		logging.Warnf("[user] invalid verification code email=%s attempts=%d", logging.RedactEmail(email), record.Attempts+1)
		return ErrInvalidVerificationCode
	}

	if err := s.codes.ClaimCode(ctx, record.ID, now.Unix()); err != nil {
		if errors.Is(err, model.ErrVerificationCodeConsumed) {
			// 另一个并发请求已经用掉了这枚码。对外与会话层保持同一口径：
			// 不区分「码不对」与「码已被用掉」。
			logging.Warnf("[user] verification code was claimed concurrently code_id=%d", record.ID)
			return ErrInvalidVerificationCode
		}
		return err
	}
	return nil
}

/* -------------------------------------------------------------------------- */
/* 辅助函数                                                                    */
/* -------------------------------------------------------------------------- */

// 哨兵错误：由 handler 映射为具体的 HTTP 状态码，不要为同一语义新增重复的错误变量。
var (
	ErrInvalidCredentials      = errors.New("invalid username or password")
	ErrInvalidVerificationCode = errors.New("invalid or expired verification code")
	ErrEmailAlreadyRegistered  = errors.New("email is already registered")
	ErrUsernameTaken           = errors.New("username is already taken")
	ErrAccountNotFound         = errors.New("account not found")
	// ErrEmailCodeAddressLimit 的文案会直接展示给访客，所以写成人话（同
	// ErrDailyEmailLimitReached）。它说明的是「这个地址今天要得太多」，
	// 刻意不提任何账号是否存在——那会变成一个账号枚举的旁路。
	ErrEmailCodeAddressLimit = errors.New("该邮箱请求验证码次数过多，请稍后再试。")
	// ErrEmailCodeTooFrequent 是「两次发送间隔不足 60 秒」。文案同样面向访客，
	// 且**必须给出还要等多久**——只说「太频繁了」会让用户反复点击试探，
	// 反而制造更多请求。给一个明确秒数，前端也能直接显示。
	ErrEmailCodeTooFrequent = errors.New("验证码发送过于频繁，请 60 秒后再试。")
)

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func normalizeAndValidateEmail(email string) (string, error) {
	email = normalizeEmail(email)
	if email == "" {
		return "", invalidf("email is required")
	}
	parsed, err := netmail.ParseAddress(email)
	if err != nil {
		return "", invalidf("invalid email address")
	}
	return parsed.Address, nil
}

// normalizeAndValidateUsername 套用登录标识的规则：去空白、检查形状与长度。
//
// 规则本体在 model 包（model.NormalizeAndValidateUsername），因为管理员登录名
// 与请求体校验也要用它——三处各写一份，迟早会有一处漏掉。这里只负责把普通
// error 翻译成 service 的 ValidationError，好让 handler 统一映射成 400。
func normalizeAndValidateUsername(username string) (string, error) {
	normalized, err := model.NormalizeAndValidateUsername(username)
	if err != nil {
		return "", invalidf("%s", err.Error())
	}
	return normalized, nil
}

// normalizeAndValidateNickname 套用公开显示名的规则：清洗后不能为空，且长度受限。
func normalizeAndValidateNickname(nickname string) (string, error) {
	nickname = normalizeNickname(nickname)
	if nickname == "" {
		return "", invalidf("nickname is required")
	}
	if len([]rune(nickname)) > model.MaxCommentNicknameLength {
		return "", invalidf("nickname must be at most %d characters", model.MaxCommentNicknameLength)
	}
	return nickname, nil
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(password)), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// HashPasswordForStorage 是 hashPassword 的导出形式，供根目录的 admin 子命令使用。
//
// 它存在的理由是「密码怎么存」这件事只能有一份实现：命令行工具若自己写一遍
// bcrypt.DefaultCost，某天服务端改了参数就会出现两个互不兼容的存储格式，
// 而症状是「用 CLI 设的密码登不进去」——非常难查。
func HashPasswordForStorage(password string) (string, error) {
	return hashPassword(password)
}

// equalizeLoginTiming 执行一次与真实密码校验等量的 bcrypt 计算，
// 使调用方无法通过测量响应耗时来区分「账号不存在」「账号无密码」与「密码错误」。
func equalizeLoginTiming(password string) {
	// The result is deliberately discarded: dummyPasswordHash belongs to a
	// throwaway password, so it can never match.
	_ = bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(password))
}

// randInt 是 crypto/rand.Int 的测试间接层。熵源失败在实践中几乎不会发生，
// 但「生成不出验证码」必须是一条可解释的错误而不是 panic；把它做成可替换的
// 变量，才能在不破坏进程环境的前提下验证那条分支。
var randInt = rand.Int

func generateVerificationCode() (string, error) {
	// 8 位十进制。长度直接决定离线枚举的成本：配合 bcrypt（见下），
	// 10^8 的搜索空间让「读出库里的哈希再还原」彻底超出验证码 10 分钟的有效期。
	n, err := randInt(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return "", fmt.Errorf("generate verification code: %w", err)
	}
	return fmt.Sprintf("%08d", n.Int64()), nil
}

// hashVerificationCode 把验证码哈希成可存储的形式。
//
// 这里**必须**用慢哈希（bcrypt），不能用无盐 SHA-256——后者对 6/8 位十进制码
// 等于明文：整个搜索空间只有 10^6 / 10^8 个候选，一台机器把 SHA-256 全跑一遍
// 只要秒级到分钟级。而 email_verification_code.go 与 RequestEmailCode 的注释
// 明确把「拿到数据库/备份的人」写进了威胁模型，无盐 SHA-256 恰好防不住它。
//
// 同一份代码里 sessions.token_hash 用的是 SHA-256 且**是安全的**，
// 因为那是 256 位随机值、没有可枚举的分布。两者的差别不在算法而在秘密的熵，
// 所以这里不能因为「会话也是这么存的」就照抄。
//
// 成本核算（bcrypt cost=10 约 60 ms/次）：10^8 × 60 ms ≈ 69 天单线程，
// 而验证码 10 分钟就过期——攻击者需要在有效期内完成，因此不可行。
func hashVerificationCode(code string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt 对超过 72 字节的输入会报错，而验证码固定是 8 位数字，
		// 正常永远走不到这里。真发生说明调用方传了别的东西进来。
		logging.Errorf("[user] hashing a verification code failed: %v", err)
		return ""
	}
	return string(hash)
}

// HashVerificationCodeForStorage 是 hashVerificationCode 的导出形式，
// 供端到端测试构造与生产完全一致的验证码记录。
//
// 与 HashPasswordForStorage 同样的理由：「验证码怎么存」只能有一份实现，
// 否则测试里的 fixture 会与服务端实际写入的格式漂移，而症状是
// 「测试全绿、真实注册却永远报验证码错误」——非常难查。
func HashVerificationCodeForStorage(code string) string {
	return hashVerificationCode(code)
}

// verifyVerificationCode 以恒定时间比对验证码与其存储形式。
// 格式不认识（例如历史遗留的无盐 SHA-256 行）一律返回 false：fail closed。
func verifyVerificationCode(stored, code string) bool {
	if stored == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(stored), []byte(code)) == nil
}
