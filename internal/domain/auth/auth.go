// Package auth 是身份认证的领域层：用户实体、密码哈希、JWT 签发/验证。
//
// 设计原则：
//   - domain 层不依赖任何 infra：bcrypt / jwt 是工具类不是基础设施
//   - User 仅含建模字段，不持有 Repo——CRUD 由 application/auth.Service 编排
//   - JWT subject = user_id（数字字符串），Claims 嵌入用户名以便日志读出来不用查库
//   - access token TTL 默认 7 天；下一轮加 refresh token 时本文件无需破坏性改动
package auth

import (
	"errors"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// ─────────────────────────────── User 实体 ─────────────────────────────────

// User 是用户领域模型；password 永远以 hash 形式持有，明文只在请求处理瞬间存在。
type User struct {
	ID           int64
	Username     string
	PasswordHash string // bcrypt 摘要，不可逆
	CreatedAt    time.Time
	LastLoginAt  time.Time
}

// ─────────────────────────────── 输入校验 ─────────────────────────────────

// 用户名 / 密码长度上下限。下限挡 brute-force，上限挡 DoS（bcrypt 输入超 72 字节会被静默截断，
// 但更长输入会让验证耗时不必要地高）。
const (
	UsernameMinLen = 3
	UsernameMaxLen = 32
	PasswordMinLen = 8
	PasswordMaxLen = 64
)

var (
	ErrUsernameTooShort = errors.New("用户名长度需 ≥ 3 字符")
	ErrUsernameTooLong  = errors.New("用户名长度需 ≤ 32 字符")
	ErrPasswordTooShort = errors.New("密码长度需 ≥ 8 字符")
	ErrPasswordTooLong  = errors.New("密码长度需 ≤ 64 字符")
	ErrUserExists       = errors.New("用户名已存在")
	ErrUserNotFound     = errors.New("用户不存在")
	ErrPasswordMismatch = errors.New("用户名或密码错误") // 故意与 NotFound 同义，避免账号枚举
	ErrInvalidToken     = errors.New("无效的访问令牌")
	ErrTokenExpired     = errors.New("访问令牌已过期")
)

// ValidateCredentials 校验注册/登录的凭证字符串。
// 用 utf8.RuneCountInString 而非 len——避免中文用户名被错误判长。
func ValidateCredentials(username, password string) error {
	un := utf8.RuneCountInString(username)
	if un < UsernameMinLen {
		return ErrUsernameTooShort
	}
	if un > UsernameMaxLen {
		return ErrUsernameTooLong
	}
	if len(password) < PasswordMinLen { // 密码用字节长度即可，bcrypt 也是按字节
		return ErrPasswordTooShort
	}
	if len(password) > PasswordMaxLen {
		return ErrPasswordTooLong
	}
	return nil
}

// ─────────────────────────────── 密码哈希 ─────────────────────────────────

// bcryptCost 12 ≈ 250ms / 现代 CPU。10 太弱，14 太慢；12 是 OWASP 推荐值。
const bcryptCost = 12

// HashPassword 用 bcrypt 生成不可逆密码摘要。
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword 验证明文密码是否匹配 bcrypt 摘要。
// 不区分"哈希格式错"和"密码不匹配"——统一返 false，避免给攻击者侧信道。
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// ─────────────────────────────── JWT ──────────────────────────────────────

// Claims 是写入 JWT 的负载。
//   - Subject = user_id（数字字符串），符合 JWT 规范
//   - Username 仅供日志/审计读取，不参与权限判定（以 Subject 为准）
//
// 复杂用例（roles / scopes）以后再加，V1 保持最小。
type Claims struct {
	jwt.RegisteredClaims
	Username string `json:"username"`
}

// TokenIssuer 用对称密钥（HS256）签发/验证 JWT。
// 配置上由调用方注入 secret——通常来自环境变量，绝不能进 git。
//
// Secret 长度建议 ≥ 32 字节随机；过短会被 brute-force。
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
	issuer string
}

// NewTokenIssuer 创建签发器。secret 太短直接拒绝构造——挡 misconfig。
func NewTokenIssuer(secret string, ttl time.Duration, issuer string) (*TokenIssuer, error) {
	if len(secret) < 32 {
		return nil, errors.New("JWT secret 至少需 32 字节，当前配置不安全")
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour // V1 默认 7 天，未来加 refresh 后可缩短
	}
	if issuer == "" {
		issuer = "agi-assistant"
	}
	return &TokenIssuer{secret: []byte(secret), ttl: ttl, issuer: issuer}, nil
}

// Sign 为 user 签发 access token。
func (t *TokenIssuer) Sign(user User) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(t.ttl)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(user.ID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			Issuer:    t.issuer,
		},
		Username: user.Username,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// Verify 解析并校验 token。返回 Claims（含 user_id / username）。
//
// 错误归一：解析失败/签名错/算法错都映射到 ErrInvalidToken；
// 仅"过期"单独区分为 ErrTokenExpired——前端可据此提示"请重新登录"。
func (t *TokenIssuer) Verify(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(tok *jwt.Token) (interface{}, error) {
		// 只允许 HS256——防御 alg=none 或非对称冒用攻击
		if tok.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return t.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrInvalidToken
	}
	if !tok.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// UserID 从 Claims 解出数字 user_id。Subject 不是数字时返 0。
func (c *Claims) UserID() int64 {
	if c == nil {
		return 0
	}
	id, _ := strconv.ParseInt(c.Subject, 10, 64)
	return id
}
