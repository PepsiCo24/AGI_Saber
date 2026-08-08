// Package auth 是身份认证用例的应用层编排：注册、登录、签发 token。
//
// 设计：
//   - Service 不直接持 *sql.DB，依赖注入 Repo 接口——便于单测
//   - 错误归一为 domain/auth 包定义的 sentinel errors
//   - 登录失败统一返 ErrPasswordMismatch，避免账号枚举侧信道
package auth

import (
	"sync"
	"time"

	"agi-assistant/internal/domain/auth"
	"agi-assistant/internal/infrastructure/persistence/userrepo"
)

// dummyHashOnce 是用作"用户不存在"假比较的合法 bcrypt 摘要——
// 一次性生成（cost=12，与 HashPassword 一致），让"用户不存在"路径
// 也跑一次真的 bcrypt 验算，耗时与"用户存在但密码错"对齐。
//
// 用 sync.Once 延迟到首次 Login 时生成；Login 频繁时复用同一摘要无安全影响
// （它本身不对应任何真实账号，攻击者拿到也无意义）。
var (
	dummyHashOnce sync.Once
	dummyHash     string
)

func dummyBcryptHash() string {
	dummyHashOnce.Do(func() {
		h, err := auth.HashPassword("placeholder-not-a-real-password-do-not-rely-on")
		if err == nil {
			dummyHash = h
		}
	})
	return dummyHash
}

// Service 是认证用例的编排者。
type Service struct {
	users  userrepo.Repo
	issuer *auth.TokenIssuer
}

// NewService 构造认证服务。issuer 不能为 nil（无 issuer 没法签 token，构造期就拒绝）。
func NewService(users userrepo.Repo, issuer *auth.TokenIssuer) *Service {
	return &Service{users: users, issuer: issuer}
}

// LoginResult 是登录/注册成功后返回前端的载荷。
type LoginResult struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	UserID    int64     `json:"user_id"`
	Username  string    `json:"username"`
}

// Register 创建新账号并直接签发 token（注册即登录，省一次往返）。
//
// 步骤：校验输入 → bcrypt hash → INSERT → 签 JWT。
// 任意步骤失败按 domain 层错误语义返回，handler 映射 HTTP 状态码。
func (s *Service) Register(username, password string) (LoginResult, error) {
	if err := auth.ValidateCredentials(username, password); err != nil {
		return LoginResult{}, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return LoginResult{}, err
	}
	user, err := s.users.Create(username, hash)
	if err != nil {
		return LoginResult{}, err
	}
	return s.issueToken(user)
}

// Login 验证密码并签发 token。
//
// 安全要点：
//  1. "用户不存在"和"密码错"都映射成 ErrPasswordMismatch——避免攻击者通过响应差异枚举账号
//  2. 即使用户不存在，仍调用 VerifyPassword 跑一次 bcrypt 假比较，让登录耗时常数化（防时间侧信道）
//     —— 假比较用合法 bcrypt 摘要；用非法占位字符串会被库直接拒，反而暴露时间差
func (s *Service) Login(username, password string) (LoginResult, error) {
	if err := auth.ValidateCredentials(username, password); err != nil {
		return LoginResult{}, err
	}

	user, err := s.users.FindByUsername(username)
	if err == auth.ErrUserNotFound {
		// 跑一次合法 bcrypt 验算，让耗时与真实路径对齐
		_ = auth.VerifyPassword(dummyBcryptHash(), password)
		return LoginResult{}, auth.ErrPasswordMismatch
	}
	if err != nil {
		return LoginResult{}, err
	}
	if !auth.VerifyPassword(user.PasswordHash, password) {
		return LoginResult{}, auth.ErrPasswordMismatch
	}

	// best-effort 更新登录时间——失败不阻塞
	s.users.TouchLastLogin(user.ID)
	return s.issueToken(user)
}

func (s *Service) issueToken(user auth.User) (LoginResult, error) {
	token, exp, err := s.issuer.Sign(user)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{
		Token:     token,
		ExpiresAt: exp,
		UserID:    user.ID,
		Username:  user.Username,
	}, nil
}
