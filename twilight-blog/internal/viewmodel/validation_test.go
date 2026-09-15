package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"blog-engine/internal/model"
)

// 这一组测试覆盖的是**公开输入的第一道闸**：请求 DTO 的 Validate()。
//
// 它在整条链上的位置很关键——service 层的规则只对「已经通过这一层」的数据生效，
// 而这一层的错误是普通 error（见包注释），handler 会把它映射成 400。
// 因此这里的每条断言都对应一个真实的滥用路径，而不是格式洁癖。

// TestPublicAuthRequestsRejectEmptyCredentials 覆盖找回密码与改密的入口。
//
// 这两条路径都涉及「用别人不知道的东西证明身份」：
//   - 找回密码需要 email + 验证码 + 新密码；
//   - 改密需要当前密码 + 新密码。
//
// 少任何一项都必须在这一层就被拒绝：空验证码若被放行，服务层就会拿它去比对，
// 而「比对空串」这件事本身不该有机会发生。
func TestPublicAuthRequestsRejectEmptyCredentials(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"reset without email", (&ResetPasswordRequest{Code: "123456", Password: "x"}).Validate(), "email and code"},
		{"reset without code", (&ResetPasswordRequest{Email: "a@b.test", Password: "x"}).Validate(), "email and code"},
		{"reset without password", (&ResetPasswordRequest{Email: "a@b.test", Code: "123456"}).Validate(), "password"},
		{"change password without current", (&ChangePasswordRequest{NewPassword: "x"}).Validate(), "current and new"},
		{"change password without new", (&ChangePasswordRequest{CurrentPassword: "x"}).Validate(), "current and new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("an incomplete request was accepted")
			}
			if !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", tc.err.Error(), tc.want)
			}
		})
	}

	// 对照组：填齐了就必须通过，否则上面那些断言可能只是「全都拒绝」。
	if err := (&ResetPasswordRequest{Email: "a@b.test", Code: "123456", Password: "x"}).Validate(); err != nil {
		t.Fatalf("a complete reset request was rejected: %v", err)
	}
	if err := (&ChangePasswordRequest{CurrentPassword: "old", NewPassword: "new"}).Validate(); err != nil {
		t.Fatalf("a complete change-password request was rejected: %v", err)
	}
}

// TestRegisterRequestRequiresEveryField 覆盖注册的必填项。
//
// 为什么逐个字段都要试一遍：这几个字段分别对应四件不同的事（收码地址、验证码、
// 登录名、展示名、口令），少校验任何一个，服务层就会收到一个缺项的形状，
// 而服务层未必有等价的守卫——那时候的表现可能是「用空用户名注册成功」。
func TestRegisterRequestRequiresEveryField(t *testing.T) {
	complete := func() *RegisterRequest {
		return &RegisterRequest{
			Email:    "visitor@example.test",
			Code:     "123456",
			Username: "visitor",
			Nickname: "访客甲",
			Password: "S3cure-passphrase",
		}
	}

	if err := complete().Validate(); err != nil {
		t.Fatalf("a complete register request was rejected: %v", err)
	}

	mutations := map[string]func(r *RegisterRequest){
		"nickname": func(r *RegisterRequest) { r.Nickname = "   " },
		"username": func(r *RegisterRequest) { r.Username = "" },
		"password": func(r *RegisterRequest) { r.Password = "   " },
		"email":    func(r *RegisterRequest) { r.Email = "" },
		"code":     func(r *RegisterRequest) { r.Code = " " },
	}
	for field, mutate := range mutations {
		t.Run("missing "+field, func(t *testing.T) {
			r := complete()
			mutate(r)
			if err := r.Validate(); err == nil {
				t.Fatalf("a register request without %s was accepted", field)
			}
		})
	}
}

// TestUpdateProfileDistinguishesOmittedFromEmpty 覆盖自助资料的指针语义：
// 「没传」= 不改，「传空串」= 会被识别为一次修改（随后由 service 层按形状规则拒绝）。
//
// ⚠️ 分层边界要写清楚：**这一层不校验取值的合法性**，它只做两件事——
// 区分「缺失」与「显式传值」，以及对传进来的值做 TrimSpace。
// 「空用户名非法」这条规则在 service 层（normalizeAndValidateUsername），
// 端到端的行为由 view 层的 TestEmptyCredentialsCannotBlankTheLoginName 守护。
//
// 之所以把这条分工写成断言而不是注释：如果哪天有人为了「更早报错」在这里加一份
// 形状校验，两份规则迟早会漂移，而漂移的那一天没人知道哪一份是对的。
func TestUpdateProfileDistinguishesOmittedFromEmpty(t *testing.T) {
	t.Run("nothing to change", func(t *testing.T) {
		if err := (&UpdateProfileRequest{}).Validate(); err == nil {
			t.Fatal("an empty update request was accepted")
		}
	})

	t.Run("nickname only is enough", func(t *testing.T) {
		nickname := "新昵称"
		if err := (&UpdateProfileRequest{Nickname: &nickname}).Validate(); err != nil {
			t.Fatalf("a nickname-only update was rejected: %v", err)
		}
	})

	t.Run("an explicit value survives as non-nil", func(t *testing.T) {
		// 空串必须原样保留「调用方显式传了值」这个信息，否则它就与「没传」不可区分，
		// 于是 service 层再也没机会拒绝它——账号的登录名会被悄悄清空。
		empty := ""
		req := &UpdateProfileRequest{Username: &empty}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if req.Username == nil {
			t.Fatal("an explicit empty username was turned into \"not provided\"; the service can no longer reject it")
		}
		if *req.Username != "" {
			t.Fatalf("username = %q, want it preserved", *req.Username)
		}
	})

	t.Run("values are trimmed", func(t *testing.T) {
		nickname := "  两边有空白  "
		req := &UpdateProfileRequest{Nickname: &nickname}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if *req.Nickname != "两边有空白" {
			t.Fatalf("nickname = %q, want it trimmed", *req.Nickname)
		}
	})
}

// TestAdminProfileRequestValidatesTheLoginName 覆盖管理员登录名的形状校验。
//
// 这是 R3 报告里 S1 那条问题的守护：前端有校验不等于服务端有校验，
// 而登录名是「能被猜中的身份标识」，必须由服务端说了算。
func TestAdminProfileRequestValidatesTheLoginName(t *testing.T) {
	valid := func() *AdminProfileRequest {
		return &AdminProfileRequest{Name: "站长"}
	}

	if err := valid().Validate(); err != nil {
		t.Fatalf("a name-only admin profile update was rejected: %v", err)
	}

	for _, bad := range []string{"ab", "1abc", "abc def", "admin\u200b", "аdmin"} {
		name := bad
		r := valid()
		r.Username = &name
		if err := r.Validate(); err == nil {
			t.Fatalf("the login name %q was accepted", name)
		}
	}

	good := "twilight"
	r := valid()
	r.Username = &good
	if err := r.Validate(); err != nil {
		t.Fatalf("a well-formed login name was rejected: %v", err)
	}

	// 展示名为空则拒绝：它不是可选项（改登录名才是）。
	if err := (&AdminProfileRequest{Name: "   "}).Validate(); err == nil {
		t.Fatal("an empty display name was accepted")
	}
}

// TestCreateCommentRequestHasNoNicknameField 钉住一条安全契约：
// **评论署名不能由请求指定**。
//
// 请求体里没有这个字段，所以「带 nickname 的评论请求」在类型层就被忽略。
// 断言方式刻意选择「发一个真的带 nickname 的 JSON，然后确认它无处可去」——
// 只检查结构体没有该字段是静态的，而这里要证的是运行时行为。
//
// 反证方式：给 CreateCommentRequest 加一个 Nickname 字段并让它参与创建 → 本条失败。
func TestCreateCommentRequestHasNoNicknameField(t *testing.T) {
	var req CreateCommentRequest
	raw := `{"target_type":"article","target_id":1,"content":"hi","nickname":"IMPOSTOR","author":"IMPOSTOR"}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("a well-formed comment request was rejected: %v", err)
	}

	// 重新序列化后不得出现任何署名相关的键——这是「请求无法影响署名」的证据。
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "IMPOSTOR") {
		t.Fatalf("the request carried an author name through: %s", encoded)
	}
}

// TestCreateCommentRequestValidatesShape 覆盖评论请求的形状校验。
//
// 注意 target_type 的**白名单**不在这里，而在 service 层（allowedCommentTargets）：
// 这一层只负责「非空、id 为正、正文长度合规」。测试把这条分工写清楚，
// 免得以后有人以为白名单在这里而把它删掉。
func TestCreateCommentRequestValidatesShape(t *testing.T) {
	if err := (&CreateCommentRequest{TargetType: "  ", TargetID: 1, Content: "x"}).Validate(); err == nil {
		t.Fatal("an empty target_type was accepted")
	}
	if err := (&CreateCommentRequest{TargetType: "article", TargetID: 0, Content: "x"}).Validate(); err == nil {
		t.Fatal("target_id = 0 was accepted")
	}
	if err := (&CreateCommentRequest{TargetType: "article", TargetID: -5, Content: "x"}).Validate(); err == nil {
		t.Fatal("a negative target_id was accepted")
	}
	if err := (&CreateCommentRequest{TargetType: "article", TargetID: 1, Content: "   "}).Validate(); err == nil {
		t.Fatal("whitespace-only content was accepted")
	}

	// 长度按**字符**而不是字节算：一个中文评论占 3 字节，
	// 若按字节判定，长度上限会变成实际可写字符数的三分之一。
	atLimit := strings.Repeat("中", model.MaxCommentContentLength)
	if err := (&CreateCommentRequest{TargetType: "article", TargetID: 1, Content: atLimit}).Validate(); err != nil {
		t.Fatalf("content of exactly %d characters was rejected: %v", model.MaxCommentContentLength, err)
	}
	overLimit := strings.Repeat("中", model.MaxCommentContentLength+1)
	if err := (&CreateCommentRequest{TargetType: "article", TargetID: 1, Content: overLimit}).Validate(); err == nil {
		t.Fatalf("content of %d characters was accepted", model.MaxCommentContentLength+1)
	}
}

// TestMomentRequestsRequireContent 记录动态与文章的一处**有意差异**：
// 动态的正文是必填的（Create 与 Update 都要求非空），因此不存在
// 「省略 content 就静默清空」那条路径——R3-15 只影响 article 与 page。
//
// 把它写成测试，是为了让这个差异成为**已知且被守护的**事实：
// 将来若把 UpdateMomentRequest.Content 改成指针以支持「只改配图」，
// 这条测试会失败，提醒改的人同时补上对应的「省略即不改」语义。
func TestMomentRequestsRequireContent(t *testing.T) {
	if err := (&CreateMomentRequest{Content: "   "}).Validate(); err == nil {
		t.Fatal("creating a moment with blank content was accepted")
	}
	if err := (&UpdateMomentRequest{Content: ""}).Validate(); err == nil {
		t.Fatal("updating a moment with blank content was accepted")
	}
	if err := (&UpdateMomentRequest{Content: "今天读了点东西"}).Validate(); err != nil {
		t.Fatalf("a valid moment update was rejected: %v", err)
	}
}
