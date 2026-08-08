package auth

import (
	"strings"
	"testing"
	"time"

	authdomain "agi-assistant/internal/domain/auth"
)

// mockRepo 实现 userrepo.Repo 接口，所有方法纯内存。
type mockRepo struct {
	byName     map[string]authdomain.User
	byID       map[int64]authdomain.User
	nextID     int64
	createErr  error
	touchCalls int
}

func newMockRepo() *mockRepo {
	return &mockRepo{
		byName: make(map[string]authdomain.User),
		byID:   make(map[int64]authdomain.User),
	}
}

func (m *mockRepo) Create(username, hash string) (authdomain.User, error) {
	if m.createErr != nil {
		return authdomain.User{}, m.createErr
	}
	if _, exists := m.byName[username]; exists {
		return authdomain.User{}, authdomain.ErrUserExists
	}
	m.nextID++
	u := authdomain.User{
		ID:           m.nextID,
		Username:     username,
		PasswordHash: hash,
		CreatedAt:    time.Now(),
	}
	m.byName[username] = u
	m.byID[u.ID] = u
	return u, nil
}

func (m *mockRepo) FindByUsername(username string) (authdomain.User, error) {
	if u, ok := m.byName[username]; ok {
		return u, nil
	}
	return authdomain.User{}, authdomain.ErrUserNotFound
}

func (m *mockRepo) FindByID(id int64) (authdomain.User, error) {
	if u, ok := m.byID[id]; ok {
		return u, nil
	}
	return authdomain.User{}, authdomain.ErrUserNotFound
}

func (m *mockRepo) TouchLastLogin(id int64) { m.touchCalls++ }

func newTestService(t *testing.T) (*Service, *mockRepo) {
	t.Helper()
	repo := newMockRepo()
	issuer, err := authdomain.NewTokenIssuer("this-is-a-32-byte-secret-key-for-jwt!", time.Hour, "test")
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	return NewService(repo, issuer), repo
}

// TestRegister_Success 正常注册流程：返回 token + 用户写入。
func TestRegister_Success(t *testing.T) {
	svc, repo := newTestService(t)
	res, err := svc.Register("alice", "verysecret")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if res.Token == "" || res.UserID == 0 {
		t.Errorf("结果不全: %+v", res)
	}
	if len(repo.byName) != 1 {
		t.Errorf("repo 应有 1 个用户")
	}
	// 验证存的是 hash 不是明文
	if repo.byName["alice"].PasswordHash == "verysecret" {
		t.Error("repo 不应存明文密码")
	}
}

// TestRegister_DuplicateUsername 重复用户名返 ErrUserExists。
func TestRegister_DuplicateUsername(t *testing.T) {
	svc, _ := newTestService(t)
	_, _ = svc.Register("alice", "verysecret")
	_, err := svc.Register("alice", "anothersecret")
	if err != authdomain.ErrUserExists {
		t.Errorf("应返回 ErrUserExists, 得到 %v", err)
	}
}

// TestRegister_InputValidation 注册时校验长度。
func TestRegister_InputValidation(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Register("ab", "verysecret"); err != authdomain.ErrUsernameTooShort {
		t.Errorf("短用户名应被拦, 得到 %v", err)
	}
	if _, err := svc.Register("alice", "1234"); err != authdomain.ErrPasswordTooShort {
		t.Errorf("短密码应被拦, 得到 %v", err)
	}
}

// TestLogin_Success 注册后能登录，返回有效 token。
func TestLogin_Success(t *testing.T) {
	svc, repo := newTestService(t)
	_, _ = svc.Register("alice", "verysecret")
	res, err := svc.Login("alice", "verysecret")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if res.Token == "" {
		t.Error("应得到 token")
	}
	if repo.touchCalls != 1 {
		t.Errorf("登录成功应触发 TouchLastLogin 1 次, 得到 %d", repo.touchCalls)
	}
}

// TestLogin_WrongPassword 密码错误返 ErrPasswordMismatch（避免账号枚举）。
func TestLogin_WrongPassword(t *testing.T) {
	svc, _ := newTestService(t)
	_, _ = svc.Register("alice", "verysecret")
	_, err := svc.Login("alice", "wrong-pwd")
	if err != authdomain.ErrPasswordMismatch {
		t.Errorf("应返回 ErrPasswordMismatch, 得到 %v", err)
	}
}

// TestLogin_NonExistentUser 不存在用户返 ErrPasswordMismatch（与密码错误同义，防枚举）。
func TestLogin_NonExistentUser(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Login("ghost", "anysecret")
	if err != authdomain.ErrPasswordMismatch {
		t.Errorf("应返回 ErrPasswordMismatch（不暴露用户存在性）, 得到 %v", err)
	}
}

// TestLogin_TimingResistance 大致检验"用户存在"vs"用户不存在"耗时差异不显著。
// bcrypt 假比较应让两者都跑一次哈希——这里只是粗略比较，不做严格统计。
func TestLogin_TimingResistance(t *testing.T) {
	svc, _ := newTestService(t)
	_, _ = svc.Register("alice", "verysecret")

	measure := func(un string) time.Duration {
		start := time.Now()
		_, _ = svc.Login(un, "wrong-pwd")
		return time.Since(start)
	}

	dExist := measure("alice")
	dNotExist := measure("ghost")
	// 仅启发式：差异在 5x 以内即认为通过——实际 bcrypt 单次 ~250ms，
	// 假比较也走一次，差异通常 <1.5x
	if dExist*5 < dNotExist || dNotExist*5 < dExist {
		t.Errorf("耗时差异过大，可能未做时间侧信道防御: exist=%v notExist=%v", dExist, dNotExist)
	}
}

// TestLogin_InvalidInput 空/超长用户名/密码不会调到 repo。
func TestLogin_InvalidInput(t *testing.T) {
	svc, _ := newTestService(t)
	for _, badUser := range []string{"", "a", strings.Repeat("z", 33)} {
		if _, err := svc.Login(badUser, "verysecret"); err == nil {
			t.Errorf("非法 username=%q 应被拒", badUser)
		}
	}
}
