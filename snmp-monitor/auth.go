package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ----------------- 口令存储 -----------------
//
// 口令只存 PBKDF2-HMAC-SHA256 的派生值, 不可逆。
// 用标准库手搓 PBKDF2 是为了不引入额外依赖(golang.org/x/crypto)。

func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, blocks*hashLen)
	var counter [4]byte

	for b := 1; b <= blocks; b++ {
		prf.Reset()
		prf.Write(salt)
		counter[0] = byte(b >> 24)
		counter[1] = byte(b >> 16)
		counter[2] = byte(b >> 8)
		counter[3] = byte(b)
		prf.Write(counter[:])
		u := prf.Sum(nil)
		t := make([]byte, hashLen)
		copy(t, u)
		for i := 2; i <= iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

const pbkdf2Iterations = 210000

// 账号/权限的存储见 users.go
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomPassword(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "change-me-now-" + randomID()
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = passwordAlphabet[int(v)%len(passwordAlphabet)]
	}
	return string(out)
}

// ----------------- 会话 -----------------

type session struct {
	user    string
	csrf    string
	expires time.Time
}

type sessionMgr struct {
	mu       sync.Mutex
	sessions map[string]*session
	ttl      time.Duration

	fails map[string]*loginFail
}

type loginFail struct {
	count int
	until time.Time
}

func newSessionMgr(ttl time.Duration) *sessionMgr {
	m := &sessionMgr{sessions: map[string]*session{}, fails: map[string]*loginFail{}, ttl: ttl}
	go m.janitor()
	return m
}

func (m *sessionMgr) janitor() {
	for range time.Tick(5 * time.Minute) {
		now := time.Now()
		m.mu.Lock()
		for k, s := range m.sessions {
			if now.After(s.expires) {
				delete(m.sessions, k)
			}
		}
		for k, f := range m.fails {
			if now.After(f.until) && f.count == 0 {
				delete(m.fails, k)
			}
		}
		m.mu.Unlock()
	}
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *sessionMgr) create(user string) (token string, csrf string) {
	token, csrf = randomToken(), randomToken()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) > 500 { // 防止异常情况下无限增长
		for k := range m.sessions {
			delete(m.sessions, k)
			break
		}
	}
	m.sessions[token] = &session{user: user, csrf: csrf, expires: time.Now().Add(m.ttl)}
	return
}

func (m *sessionMgr) get(token string) (*session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return nil, false
	}
	if time.Now().After(s.expires) {
		delete(m.sessions, token)
		return nil, false
	}
	s.expires = time.Now().Add(m.ttl) // 活跃即续期
	return s, true
}

func (m *sessionMgr) destroy(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}

// 登录失败节流: 同一来源连续失败 5 次后锁定 5 分钟, 防口令爆破
func (m *sessionMgr) throttled(ip string) (bool, time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.fails[ip]
	if !ok {
		return false, 0
	}
	if time.Now().Before(f.until) {
		return true, time.Until(f.until)
	}
	return false, 0
}

func (m *sessionMgr) noteFail(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.fails[ip]
	if !ok {
		f = &loginFail{}
		m.fails[ip] = f
	}
	f.count++
	if f.count >= 5 {
		f.until = time.Now().Add(5 * time.Minute)
		f.count = 0
	}
}

func (m *sessionMgr) noteSuccess(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.fails, ip)
}

// ----------------- HTTP 辅助 -----------------

const sessionCookie = "netmon_session"

func clientIP(r *http.Request) string {
	// 只信任直连地址。X-Forwarded-For 可被伪造, 用它做节流等于没节流。
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(ttl),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		MaxAge: -1, SameSite: http.SameSiteLaxMode,
	})
}

func currentSession(m *sessionMgr, r *http.Request) (*session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	return m.get(c.Value)
}

// checkCSRF 对所有写操作校验双重提交令牌
func checkCSRF(s *session, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(r.Header.Get("X-CSRF-Token"))), []byte(s.csrf)) == 1
}

// setTTL 在页面上改了登录有效期后调用, 对之后的续期/新登录生效。
func (m *sessionMgr) setTTL(ttl time.Duration) {
	m.mu.Lock()
	m.ttl = ttl
	m.mu.Unlock()
}

func (m *sessionMgr) getTTL() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ttl
}
