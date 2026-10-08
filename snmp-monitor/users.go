package main

// 账号与权限(v12)。
//
//   - 一个管理员(admin)+ 任意个子账号。管理员拥有全部权限, 并且只有管理员能在
//     「👥 账号权限」里增删子账号、分配权限、重置口令、停用账号。
//   - 权限按功能模块划分(见 permDefs), 子账号只能做被勾选的那几类写操作;
//     所有查看类页面(仪表盘/拓扑/机房/大屏)仍然不需要登录。
//   - 权限每次请求都现查账号表, 管理员改完立刻生效; 账号被停用/删除/重置口令时
//     它已登录的会话一并作废。
//   - v11 及以前的 auth.json 只有一个账号(user/salt/hash), 读到时自动迁移成
//     管理员, 口令不变。
//
// 口令只存 PBKDF2-HMAC-SHA256 的派生值, 不可逆(见 auth.go)。

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	roleAdmin = "admin"
	roleUser  = "user"
)

// permDef 是一类可以单独授权的写操作。
type permDef struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Desc string `json:"desc"`
}

var permDefs = []permDef{
	{"devices", "设备管理", "添加/修改/删除被监控设备、测试连通性、立即刷新"},
	{"alerts", "告警处置", "确认、关闭告警"},
	{"topology", "拓扑编辑", "拖动/编排拓扑、手工节点和链路、隐藏链路、底图"},
	{"datacenter", "机房 / 机柜", "编辑机房、冷通道、机柜布局, 设备上架, 设备形态"},
	{"backup_view", "查看配置备份", "查看、对比、下载设备配置(配置里可能有敏感信息)"},
	{"backup", "执行配置备份", "立即备份、上传、删除备份, 修改 SSH 凭据和备份计划"},
	{"config_push", "配置下发", "向设备下发 sFlow / syslog / NetFlow 配置、执行自定义命令(用配置备份里的登录凭据)"},
	{"reports", "巡检报告", "生成、删除报告, 修改报告设置"},
	{"settings", "系统设置", "修改系统设置、厂商 OID 规则、流量采集设置"},
}

func validPerm(p string) bool {
	for _, d := range permDefs {
		if d.Key == p {
			return true
		}
	}
	return false
}

type authUser struct {
	User      string   `json:"user"`
	Salt      string   `json:"salt"`
	Hash      string   `json:"hash"`
	Iter      int      `json:"iter"`
	Role      string   `json:"role"`
	Perms     []string `json:"perms,omitempty"`
	Disabled  bool     `json:"disabled,omitempty"`
	Note      string   `json:"note,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at"`
	LastLogin string   `json:"last_login,omitempty"`
}

// authFile 是 auth.json 的格式。User/Salt/Hash/Iter 是 v11 及以前的单账号格式, 只读不写。
type authFile struct {
	Users []authUser `json:"users,omitempty"`
	// Guest: 游客(未登录)可见范围, 没有 = 全部开放(升级前的行为), 见 guest.go
	Guest *guestPolicy `json:"guest,omitempty"`

	User      string `json:"user,omitempty"`
	Salt      string `json:"salt,omitempty"`
	Hash      string `json:"hash,omitempty"`
	Iter      int    `json:"iter,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type authStore struct {
	mu    sync.RWMutex
	path  string
	users []authUser
	guest *guestPolicy
}

func newAuthStore(dir string) *authStore {
	return &authStore{path: filepath.Join(dir, "auth.json")}
}

func nowStr() string { return time.Now().Format("2006-01-02 15:04:05") }

// load 读取账号文件; 不存在就生成管理员账号和随机初始口令, 打印到控制台(仅此一次)。
func (a *authStore) load(defaultUser string) error {
	var f authFile
	err := readJSON(a.path, &f)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		users := f.Users
		if len(users) == 0 && f.Hash != "" && f.Salt != "" {
			users = []authUser{{User: f.User, Salt: f.Salt, Hash: f.Hash, Iter: f.Iter, Role: roleAdmin, UpdatedAt: f.UpdatedAt}}
		}
		if len(users) > 0 {
			a.mu.Lock()
			a.users = users
			if f.Guest != nil {
				g := f.Guest.normalized()
				a.guest = &g
			}
			a.fixLocked()
			a.mu.Unlock()
			if len(f.Users) == 0 {
				return a.save() // 迁移成新格式
			}
			return nil
		}
	}
	pw := randomPassword(16)
	if err := a.setPassword(defaultUser, pw); err != nil {
		return err
	}
	fmt.Println("────────────────────────────────────────────────────────────")
	fmt.Printf("🔐 首次启动, 已生成管理员账号\n")
	fmt.Printf("    用户名: %s\n", defaultUser)
	fmt.Printf("    初始口令: %s\n", pw)
	fmt.Printf("    口令只在这里显示这一次, 请立刻登录后在页面上修改。\n")
	fmt.Println("────────────────────────────────────────────────────────────")
	return nil
}

// fixLocked 兜底: 至少要有一个启用的管理员, 否则把第一个账号提成管理员(手改文件改坏了也能登录)。
func (a *authStore) fixLocked() {
	for i := range a.users {
		if a.users[i].Role != roleAdmin {
			a.users[i].Role = roleUser
		}
	}
	for _, u := range a.users {
		if u.Role == roleAdmin && !u.Disabled {
			return
		}
	}
	if len(a.users) > 0 {
		a.users[0].Role, a.users[0].Disabled = roleAdmin, false
	}
}

func (a *authStore) save() error {
	a.mu.RLock()
	f := authFile{Users: append([]authUser(nil), a.users...), Guest: a.guest}
	a.mu.RUnlock()
	return atomicWriteJSON(a.path, f, 0o600)
}

func hashPassword(password string) (salt, hash string, err error) {
	if len(password) < 8 {
		return "", "", fmt.Errorf("口令至少 8 位")
	}
	if len(password) > 256 {
		return "", "", fmt.Errorf("口令过长")
	}
	s := make([]byte, 16)
	if _, err := rand.Read(s); err != nil {
		return "", "", err
	}
	h := pbkdf2SHA256([]byte(password), s, pbkdf2Iterations, 32)
	return base64.StdEncoding.EncodeToString(s), base64.StdEncoding.EncodeToString(h), nil
}

func (a *authStore) indexLocked(user string) int {
	for i, u := range a.users {
		if strings.EqualFold(u.User, user) {
			return i
		}
	}
	return -1
}

// setPassword 改口令。账号表还是空的(首次启动)时直接建成管理员。
func (a *authStore) setPassword(user, password string) error {
	salt, hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	a.mu.Lock()
	i := a.indexLocked(user)
	switch {
	case i >= 0:
		a.users[i].Salt, a.users[i].Hash, a.users[i].Iter, a.users[i].UpdatedAt = salt, hash, pbkdf2Iterations, nowStr()
	case len(a.users) == 0:
		a.users = append(a.users, authUser{User: user, Salt: salt, Hash: hash, Iter: pbkdf2Iterations, Role: roleAdmin, CreatedAt: nowStr(), UpdatedAt: nowStr()})
	default:
		a.mu.Unlock()
		return fmt.Errorf("账号不存在")
	}
	a.mu.Unlock()
	return a.save()
}

// verify 校验口令, 成功时返回规范写法的用户名(登录名不区分大小写)。停用的账号不能登录。
func (a *authStore) verify(user, password string) bool {
	_, ok := a.check(user, password)
	return ok
}

func (a *authStore) check(user, password string) (string, bool) {
	a.mu.RLock()
	i := a.indexLocked(strings.TrimSpace(user))
	var u authUser
	if i >= 0 {
		u = a.users[i]
	}
	a.mu.RUnlock()
	// 账号不存在时也照样算一遍 PBKDF2, 不让响应时间泄露"用户名是否存在"
	salt, _ := base64.StdEncoding.DecodeString(u.Salt)
	want, _ := base64.StdEncoding.DecodeString(u.Hash)
	if len(salt) == 0 {
		salt = []byte("netmon-no-such-user")
	}
	iter := u.Iter
	if iter <= 0 {
		iter = pbkdf2Iterations
	}
	n := len(want)
	if n == 0 {
		n = 32
	}
	got := pbkdf2SHA256([]byte(password), salt, iter, n)
	if i < 0 || u.Hash == "" || u.Disabled {
		return "", false
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return "", false
	}
	return u.User, true
}

// username 返回第一个管理员的用户名(兼容旧代码)。
func (a *authStore) username() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, u := range a.users {
		if u.Role == roleAdmin {
			return u.User
		}
	}
	return ""
}

func (a *authStore) touchLogin(user string) {
	a.mu.Lock()
	if i := a.indexLocked(user); i >= 0 {
		a.users[i].LastLogin = nowStr()
	}
	a.mu.Unlock()
	_ = a.save()
}

// access 返回账号当前的角色和权限; 账号不存在或已停用时 ok=false。
func (a *authStore) access(user string) (role string, perms map[string]bool, ok bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	i := a.indexLocked(user)
	if i < 0 || a.users[i].Disabled {
		return "", nil, false
	}
	u := a.users[i]
	perms = map[string]bool{}
	for _, d := range permDefs {
		if u.Role == roleAdmin {
			perms[d.Key] = true
		}
	}
	for _, p := range u.Perms {
		if validPerm(p) {
			perms[p] = true
		}
	}
	return u.Role, perms, true
}

func (a *authStore) can(user, perm string) bool {
	role, perms, ok := a.access(user)
	return ok && (role == roleAdmin || perms[perm])
}

// userView 是给页面看的账号信息(不含口令派生值)。
type userView struct {
	User      string   `json:"user"`
	Role      string   `json:"role"`
	Perms     []string `json:"perms"`
	Disabled  bool     `json:"disabled"`
	Note      string   `json:"note"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	LastLogin string   `json:"last_login"`
}

func (a *authStore) list() []userView {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]userView, 0, len(a.users))
	for _, u := range a.users {
		p := append([]string{}, u.Perms...)
		sort.Strings(p)
		out = append(out, userView{User: u.User, Role: u.Role, Perms: p, Disabled: u.Disabled, Note: u.Note, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt, LastLogin: u.LastLogin})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Role == roleAdmin) != (out[j].Role == roleAdmin) {
			return out[i].Role == roleAdmin
		}
		return naturalLess(out[i].User, out[j].User)
	})
	return out
}

var userNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-@\p{Han}]{2,32}$`)

type userSaveReq struct {
	User     string   `json:"user"`
	New      bool     `json:"new"`
	Password string   `json:"password"` // 新建必填; 修改时填了就是重置口令
	Role     string   `json:"role"`
	Perms    []string `json:"perms"`
	Disabled bool     `json:"disabled"`
	Note     string   `json:"note"`
}

// upsert 由管理员调用。返回 true 表示这个账号已登录的会话要作废(停用 / 重置口令 / 降权)。
func (a *authStore) upsert(req userSaveReq, by string) (kick bool, err error) {
	req.User = strings.TrimSpace(req.User)
	req.Note = strings.TrimSpace(req.Note)
	if len([]rune(req.Note)) > 60 {
		return false, fmt.Errorf("备注最多 60 个字")
	}
	if req.Role != roleAdmin {
		req.Role = roleUser
	}
	perms := []string{}
	seen := map[string]bool{}
	for _, p := range req.Perms {
		if !validPerm(p) {
			return false, fmt.Errorf("未知权限: %s", p)
		}
		if !seen[p] {
			seen[p] = true
			perms = append(perms, p)
		}
	}
	if req.Role == roleAdmin {
		perms = nil
	}
	var salt, hash string
	if req.Password != "" || req.New {
		if salt, hash, err = hashPassword(req.Password); err != nil {
			return false, err
		}
	}
	a.mu.Lock()
	defer func() {
		a.mu.Unlock()
		if err == nil {
			err = a.save()
		}
	}()
	i := a.indexLocked(req.User)
	if req.New {
		if !userNameRe.MatchString(req.User) {
			return false, fmt.Errorf("用户名 2-32 位, 只能用字母、数字、汉字和 _ . - @")
		}
		if i >= 0 {
			return false, fmt.Errorf("用户名「%s」已存在", req.User)
		}
		if len(a.users) >= 200 {
			return false, fmt.Errorf("账号太多了(上限 200)")
		}
		a.users = append(a.users, authUser{User: req.User, Salt: salt, Hash: hash, Iter: pbkdf2Iterations, Role: req.Role, Perms: perms,
			Disabled: req.Disabled, Note: req.Note, CreatedAt: nowStr(), UpdatedAt: nowStr()})
		return false, nil
	}
	if i < 0 {
		return false, fmt.Errorf("账号不存在")
	}
	u := &a.users[i]
	self := strings.EqualFold(u.User, by)
	if self && (req.Disabled || req.Role != roleAdmin) {
		return false, fmt.Errorf("不能停用自己, 也不能取消自己的管理员身份")
	}
	if u.Role == roleAdmin && (req.Role != roleAdmin || req.Disabled) && a.adminCountLocked() <= 1 {
		return false, fmt.Errorf("至少要保留一个启用的管理员")
	}
	kick = (req.Disabled && !u.Disabled) || hash != "" || (u.Role == roleAdmin && req.Role != roleAdmin)
	u.Role, u.Perms, u.Disabled, u.Note, u.UpdatedAt = req.Role, perms, req.Disabled, req.Note, nowStr()
	if hash != "" {
		u.Salt, u.Hash, u.Iter = salt, hash, pbkdf2Iterations
	}
	if self {
		kick = false // 管理员给自己重置口令: 不把自己踢下线
	}
	return kick, nil
}

func (a *authStore) adminCountLocked() int {
	n := 0
	for _, u := range a.users {
		if u.Role == roleAdmin && !u.Disabled {
			n++
		}
	}
	return n
}

func (a *authStore) remove(user, by string) error {
	a.mu.Lock()
	i := a.indexLocked(user)
	if i < 0 {
		a.mu.Unlock()
		return fmt.Errorf("账号不存在")
	}
	if strings.EqualFold(a.users[i].User, by) {
		a.mu.Unlock()
		return fmt.Errorf("不能删除自己")
	}
	if a.users[i].Role == roleAdmin && !a.users[i].Disabled && a.adminCountLocked() <= 1 {
		a.mu.Unlock()
		return fmt.Errorf("至少要保留一个启用的管理员")
	}
	a.users = append(a.users[:i], a.users[i+1:]...)
	a.mu.Unlock()
	return a.save()
}

// destroyUser 作废某个账号的全部会话(停用/删除/重置口令后调用)。
func (m *sessionMgr) destroyUser(user string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sessions {
		if strings.EqualFold(s.user, user) {
			delete(m.sessions, k)
		}
	}
}

// ----------------- HTTP -----------------

// requirePerm 在 requireAuth 之上再校验功能权限。
func (s *server) requirePerm(perm string, fn func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request, sess *session) {
		if !s.auth.can(sess.user, perm) {
			name := perm
			for _, d := range permDefs {
				if d.Key == perm {
					name = d.Name
				}
			}
			s.fail(w, http.StatusForbidden, "没有「"+name+"」权限, 请联系管理员分配")
			return
		}
		fn(w, r, sess)
	})
}

func (s *server) requireAdmin(fn func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request, sess *session) {
		if role, _, ok := s.auth.access(sess.user); !ok || role != roleAdmin {
			s.fail(w, http.StatusForbidden, "只有管理员可以管理账号")
			return
		}
		fn(w, r, sess)
	})
}

func (s *server) handleUsers(w http.ResponseWriter, r *http.Request, sess *session) {
	s.writeJSON(w, 200, map[string]interface{}{"users": s.auth.list(), "perms": permDefs, "me": sess.user, "guest": s.auth.guestPolicy(), "views": viewDefs})
}

func (s *server) handleUserSave(w http.ResponseWriter, r *http.Request, sess *session) {
	var req userSaveReq
	if !s.decode(w, r, &req) {
		return
	}
	kick, err := s.auth.upsert(req, sess.user)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if kick {
		s.sessions.destroyUser(req.User)
	}
	fmt.Printf("👥 [%s] %s %s了账号 %s\n", time.Now().Format("15:04:05"), sess.user, map[bool]string{true: "新建", false: "修改"}[req.New], req.User)
	s.writeJSON(w, 200, map[string]interface{}{"status": "ok", "users": s.auth.list()})
}

func (s *server) handleUserDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		User string `json:"user"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.auth.remove(req.User, sess.user); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.sessions.destroyUser(req.User)
	fmt.Printf("👥 [%s] %s 删除了账号 %s\n", time.Now().Format("15:04:05"), sess.user, req.User)
	s.writeJSON(w, 200, map[string]interface{}{"status": "ok", "users": s.auth.list()})
}
