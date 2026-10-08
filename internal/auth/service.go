// Package auth holds panel users, roles, sessions and sign-in.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	// SessionCookie is the cookie that holds a session's token.
	SessionCookie = "rsm_session"
	// SessionTTL is how long a session lasts from sign-in.
	SessionTTL = 14 * 24 * time.Hour
)

var (
	// ErrBadLogin is a sign-in that failed, saying no more than that.
	ErrBadLogin = errors.New("wrong username or password")
	// ErrNotFound is a user, role or session that isn't there.
	ErrNotFound = errors.New("not found")
	// ErrLastOwner refuses a change that would leave no owner who can sign in.
	ErrLastOwner = errors.New("the last owner cannot be removed or demoted")
	// ErrSetUp refuses a setup once the panel has users.
	ErrSetUp = errors.New("the panel is already set up")
	// ErrSteamTaken refuses linking a Steam account another user has.
	ErrSteamTaken = errors.New("that Steam account is linked to another user")
)

var (
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{3,32}$`)
	steamIDRe  = regexp.MustCompile(`^7656119\d{10}$`)
)

// Grant gives a user a role, on one instance or, as "*", on all.
type Grant struct {
	RoleID   int64  `json:"roleId"`
	Role     string `json:"role"`
	Instance string `json:"instance"` // "*" or an instance id
}

// User is a panel user, with the roles they hold.
type User struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	SteamID     string  `json:"steamId,omitempty"`
	Owner       bool    `json:"owner"`
	Disabled    bool    `json:"disabled"`
	HasPassword bool    `json:"hasPassword"`
	CreatedAt   int64   `json:"createdAt"`
	LastLoginAt int64   `json:"lastLoginAt,omitempty"`
	Grants      []Grant `json:"grants"`
}

// Role is a named set of permissions. The built-in ones are made with a new
// database, and can be changed like any other.
type Role struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	Builtin     bool     `json:"builtin"`
}

// Service keeps the panel's users, roles and sessions, and signs users in.
type Service struct {
	db *sql.DB
	// Steam signs users in through Steam.
	Steam *Steam

	mu       sync.Mutex
	setupPIN string
	// byIP counts failed sign-ins, setups and Steam sign-ins per address,
	// byUser failed password sign-ins per account, from any address.
	byIP, byUser *limiter
	// setupMu holds one setup at a time, so two requests with the PIN cannot
	// both create an owner.
	setupMu sync.Mutex
}

// New opens the service over the manager's database, adding the built-in
// roles to a new one.
func New(ctx context.Context, db *sql.DB) (*Service, error) {
	s := &Service{db: db, Steam: NewSteam(), byIP: newLimiter(10, 5*time.Minute), byUser: newLimiter(20, 15*time.Minute)}
	if err := s.seedRoles(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) seedRoles(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM roles").Scan(&n); err != nil || n > 0 {
		return err
	}
	for _, r := range builtinRoles {
		perms, _ := json.Marshal(r.perms)
		if _, err := s.db.ExecContext(ctx, "INSERT INTO roles(name, permissions, builtin) VALUES(?, ?, 1)", r.name, string(perms)); err != nil {
			return err
		}
	}
	return nil
}

// ---- first-run setup ----

// SetupRequired reports whether the panel has no users yet.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// SetupPIN returns the one-time PIN that claims the panel, creating it on first call.
func (s *Service) SetupPIN() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setupPIN == "" {
		s.setupPIN = randomDigits(8)
	}
	return s.setupPIN
}

// Setup creates the owner account. The PIN is printed only to the manager's
// own console. Wrong PINs count against the address they came from.
func (s *Service) Setup(ctx context.Context, ip, pin, username, password string) (*User, error) {
	now := time.Now()
	key := "setup:" + ipKey(ip)
	if wait := s.byIP.wait(key, now); wait > 0 {
		return nil, &TooManyAttemptsError{Wait: wait}
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	required, err := s.SetupRequired(ctx)
	if err != nil {
		return nil, err
	}
	if !required {
		return nil, ErrSetUp
	}
	s.mu.Lock()
	ok := s.setupPIN != "" && constantEq(pin, s.setupPIN)
	s.mu.Unlock()
	if !ok {
		s.byIP.fail(key, now)
		return nil, errors.New("wrong setup PIN")
	}
	u, err := s.CreateUser(ctx, NewUser{Username: username, Password: password, Owner: true})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.setupPIN = ""
	s.mu.Unlock()
	return u, nil
}

// ---- users ----

// NewUser is a user to create: a password, a linked Steam account or both,
// and the roles they hold.
type NewUser struct {
	Username string
	Password string
	SteamID  string
	Owner    bool
	Grants   []Grant
}

// CreateUser adds a user with their grants, all or nothing.
func (s *Service) CreateUser(ctx context.Context, n NewUser) (*User, error) {
	if !usernameRe.MatchString(n.Username) {
		return nil, errors.New("usernames are 3-32 letters, digits, '.', '_' or '-'")
	}
	if n.Password == "" && n.SteamID == "" {
		return nil, errors.New("a user needs a password or a linked Steam account")
	}
	var hash any
	if n.Password != "" {
		if err := ValidatePassword(n.Password); err != nil {
			return nil, err
		}
		h, err := HashPassword(n.Password)
		if err != nil {
			return nil, err
		}
		hash = h
	}
	var sid any
	if n.SteamID != "" {
		if !steamIDRe.MatchString(n.SteamID) {
			return nil, errors.New("not a SteamID64")
		}
		sid = n.SteamID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "INSERT INTO users(username, password_hash, steam_id, is_owner, created_at) VALUES(?, ?, ?, ?, ?)",
		n.Username, hash, sid, n.Owner, time.Now().Unix())
	if isUnique(err) {
		return nil, errors.New("that username or Steam account is already used")
	}
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := setGrants(ctx, tx, id, n.Grants); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.User(ctx, id)
}

// setGrants replaces a user's grants. An empty instance means all of them.
func setGrants(ctx context.Context, tx *sql.Tx, userID int64, grants []Grant) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_roles WHERE user_id = ?", userID); err != nil {
		return err
	}
	for _, g := range grants {
		inst := g.Instance
		if inst == "" {
			inst = "*"
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO user_roles(user_id, role_id, instance_id) VALUES(?, ?, ?)", userID, g.RoleID, inst); err != nil {
			return fmt.Errorf("grant role %d: %w", g.RoleID, err)
		}
	}
	return nil
}

const userCols = "id, username, steam_id, is_owner, disabled, password_hash IS NOT NULL, created_at, last_login_at"

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var steam sql.NullString
	var last sql.NullInt64
	if err := row.Scan(&u.ID, &u.Username, &steam, &u.Owner, &u.Disabled, &u.HasPassword, &u.CreatedAt, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.SteamID, u.LastLoginAt = steam.String, last.Int64
	u.Grants = []Grant{}
	return &u, nil
}

// User finds a user by ID, with their grants.
func (s *Service) User(ctx context.Context, id int64) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	u.Grants, err = s.grants(ctx, id)
	return u, err
}

// Users lists every user by name, with their grants.
func (s *Service) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, u := range out {
		if u.Grants, err = s.grants(ctx, u.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) grants(ctx context.Context, userID int64) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ur.role_id, r.name, ur.instance_id FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = ? ORDER BY r.name, ur.instance_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.RoleID, &g.Role, &g.Instance); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UserUpdate is a change to a user: each field set is changed, each nil left.
type UserUpdate struct {
	Username *string  `json:"username"`
	Password *string  `json:"password"`
	SteamID  *string  `json:"steamId"` // "" unlinks
	Owner    *bool    `json:"owner"`
	Disabled *bool    `json:"disabled"`
	Grants   *[]Grant `json:"grants"`
}

// UpdateUser applies the fields of up that are set. A new password or
// disabling the user ends all of their sessions.
func (s *Service) UpdateUser(ctx context.Context, id int64, up UserUpdate) (*User, error) {
	u, err := s.User(ctx, id)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if up.Username != nil {
		if !usernameRe.MatchString(*up.Username) {
			return nil, errors.New("usernames are 3-32 letters, digits, '.', '_' or '-'")
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET username = ? WHERE id = ?", *up.Username, id)
		if isUnique(err) {
			return nil, errors.New("that username is already used")
		}
		if err != nil {
			return nil, err
		}
	}
	if up.Password != nil {
		if err := ValidatePassword(*up.Password); err != nil {
			return nil, err
		}
		h, err := HashPassword(*up.Password)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", h, id); err != nil {
			return nil, err
		}
		// Or whoever learned the old password stays signed in.
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", id); err != nil {
			return nil, err
		}
	}
	if up.SteamID != nil {
		var v any
		if *up.SteamID != "" {
			if !steamIDRe.MatchString(*up.SteamID) {
				return nil, errors.New("not a SteamID64")
			}
			v = *up.SteamID
		} else if !u.HasPassword && up.Password == nil {
			return nil, errors.New("set a password before unlinking Steam, or the user cannot sign in")
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET steam_id = ? WHERE id = ?", v, id)
		if isUnique(err) {
			return nil, ErrSteamTaken
		}
		if err != nil {
			return nil, err
		}
		// The Steam account taken off the user may be one someone else now
		// holds, so the sessions it could have opened end with it.
		if u.SteamID != "" && *up.SteamID != u.SteamID {
			if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", id); err != nil {
				return nil, err
			}
		}
	}
	demote := (up.Owner != nil && !*up.Owner) || (up.Disabled != nil && *up.Disabled)
	if u.Owner && demote {
		if err := lastOwnerCheck(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	if up.Owner != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE users SET is_owner = ? WHERE id = ?", *up.Owner, id); err != nil {
			return nil, err
		}
	}
	if up.Disabled != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE users SET disabled = ? WHERE id = ?", *up.Disabled, id); err != nil {
			return nil, err
		}
		if *up.Disabled {
			if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", id); err != nil {
				return nil, err
			}
		}
	}
	if up.Grants != nil {
		if err := setGrants(ctx, tx, id, *up.Grants); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.User(ctx, id)
}

func lastOwnerCheck(ctx context.Context, tx *sql.Tx, id int64) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_owner = 1 AND disabled = 0 AND id != ?", id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrLastOwner
	}
	return nil
}

// DeleteUser removes a user, with their grants and sessions, unless they are
// the last owner.
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner bool
	if err := tx.QueryRowContext(ctx, "SELECT is_owner FROM users WHERE id = ?", id).Scan(&owner); err != nil {
		return ErrNotFound
	}
	if owner {
		if err := lastOwnerCheck(ctx, tx, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// RoleHolders lists the users granted a role, on any server.
func (s *Service) RoleHolders(ctx context.Context, roleID int64) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT user_id FROM user_roles WHERE role_id = ? ORDER BY user_id", roleID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]*User, 0, len(ids))
	for _, id := range ids {
		u, err := s.User(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// Perms resolves what a user may do.
func (s *Service) Perms(ctx context.Context, u *User) (Perms, error) {
	p := Perms{Owner: u.Owner, Global: map[string]bool{}, ByInstance: map[string]map[string]bool{}}
	if u.Owner {
		return p, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.permissions, ur.instance_id FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = ?`, u.ID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, inst string
		if err := rows.Scan(&raw, &inst); err != nil {
			return p, err
		}
		var keys []string
		_ = json.Unmarshal([]byte(raw), &keys)
		p.add(keys, inst)
	}
	return p, rows.Err()
}

// GrantPerms is what a user holding exactly these grants could do.
func (s *Service) GrantPerms(ctx context.Context, grants []Grant) (Perms, error) {
	p := Perms{Global: map[string]bool{}, ByInstance: map[string]map[string]bool{}}
	roles, err := s.Roles(ctx)
	if err != nil {
		return p, err
	}
	byID := map[int64][]string{}
	for _, r := range roles {
		byID[r.ID] = r.Permissions
	}
	for _, g := range grants {
		keys, ok := byID[g.RoleID]
		if !ok {
			return p, fmt.Errorf("no role %d", g.RoleID)
		}
		inst := g.Instance
		if inst == "" {
			inst = "*"
		}
		p.add(keys, inst)
	}
	return p, nil
}

// Role returns one role.
func (s *Service) Role(ctx context.Context, id int64) (Role, error) {
	roles, err := s.Roles(ctx)
	if err != nil {
		return Role{}, err
	}
	for _, r := range roles {
		if r.ID == id {
			return r, nil
		}
	}
	return Role{}, ErrNotFound
}

// ---- roles ----

// Roles lists the roles, built-in ones first.
func (s *Service) Roles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, permissions, builtin FROM roles ORDER BY builtin DESC, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		var r Role
		var raw string
		if err := rows.Scan(&r.ID, &r.Name, &raw, &r.Builtin); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &r.Permissions)
		out = append(out, r)
	}
	return out, rows.Err()
}

func cleanPerms(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, k := range in {
		if !validPerm(k) {
			return nil, fmt.Errorf("unknown permission %q", k)
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// SaveRole adds r, or with an ID changes that role, and returns it as saved.
func (s *Service) SaveRole(ctx context.Context, r Role) (Role, error) {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 48 {
		return r, errors.New("role names are 1-48 characters")
	}
	perms, err := cleanPerms(r.Permissions)
	if err != nil {
		return r, err
	}
	r.Permissions = perms
	raw, _ := json.Marshal(perms)
	if r.ID == 0 {
		res, err := s.db.ExecContext(ctx, "INSERT INTO roles(name, permissions) VALUES(?, ?)", r.Name, string(raw))
		if isUnique(err) {
			return r, errors.New("a role with that name exists")
		}
		if err != nil {
			return r, err
		}
		r.ID, err = res.LastInsertId()
		return r, err
	}
	res, err := s.db.ExecContext(ctx, "UPDATE roles SET name = ?, permissions = ? WHERE id = ?", r.Name, string(raw), r.ID)
	if isUnique(err) {
		return r, errors.New("a role with that name exists")
	}
	if err != nil {
		return r, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return r, ErrNotFound
	}
	return r, nil
}

// DeleteRole removes a role, and every grant of it.
func (s *Service) DeleteRole(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM roles WHERE id = ?", id)
	return err
}

// ---- sign-in and sessions ----

// Login checks a username and password. A failure counts against the address
// it came from and, for an account that exists, against the account, so
// neither one address nor many can keep guessing at a password; a refused
// attempt returns a *TooManyAttemptsError.
func (s *Service) Login(ctx context.Context, ip, username, password string) (*User, error) {
	now := time.Now()
	ipk := "login:" + ipKey(ip)
	if wait := s.byIP.wait(ipk, now); wait > 0 {
		return nil, &TooManyAttemptsError{Wait: wait}
	}
	var id int64
	var hash sql.NullString
	var disabled bool
	err := s.db.QueryRowContext(ctx, "SELECT id, password_hash, disabled FROM users WHERE username = ?", username).Scan(&id, &hash, &disabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !hash.Valid) {
		CheckPassword(dummyHash, password)
		s.byIP.fail(ipk, now)
		return nil, ErrBadLogin
	}
	if err != nil {
		return nil, err
	}
	userk := "user:" + strconv.FormatInt(id, 10)
	if wait := s.byUser.wait(userk, now); wait > 0 {
		return nil, &TooManyAttemptsError{Wait: wait}
	}
	if !CheckPassword(hash.String, password) {
		s.byIP.fail(ipk, now)
		s.byUser.fail(userk, now)
		return nil, ErrBadLogin
	}
	if disabled {
		return nil, ErrBadLogin
	}
	return s.User(ctx, id)
}

// SteamSignIn checks the assertion a Steam sign-in came back with and gives
// the SteamID64 it proves. Rejected ones count against the address, as failed
// sign-ins do.
func (s *Service) SteamSignIn(ctx context.Context, ip string, query url.Values, returnTo string) (string, error) {
	now := time.Now()
	key := "steam:" + ipKey(ip)
	if wait := s.byIP.wait(key, now); wait > 0 {
		return "", &TooManyAttemptsError{Wait: wait}
	}
	id, err := s.Steam.Verify(ctx, query, returnTo)
	if errors.Is(err, ErrSteamRejected) || errors.Is(err, ErrSteamStale) {
		s.byIP.fail(key, now)
	}
	return id, err
}

// UserBySteam finds the enabled user a Steam account is linked to.
func (s *Service) UserBySteam(ctx context.Context, steamID string) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE steam_id = ?", steamID))
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrNotFound
	}
	u.Grants, err = s.grants(ctx, u.ID)
	return u, err
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// NewSession returns the raw token for the cookie; only its hash is stored.
func (s *Service) NewSession(ctx context.Context, userID int64, ip, ua string) (string, error) {
	token := rand.Text()
	now := time.Now()
	if len(ua) > 256 {
		ua = ua[:256]
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO sessions(token_hash, user_id, created_at, expires_at, last_seen_at, ip, user_agent) VALUES(?, ?, ?, ?, ?, ?, ?)",
		hashToken(token), userID, now.Unix(), now.Add(SessionTTL).Unix(), now.Unix(), ip, ua)
	if err != nil {
		return "", err
	}
	_, _ = s.db.ExecContext(ctx, "UPDATE users SET last_login_at = ? WHERE id = ?", now.Unix(), userID)
	_, _ = s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", now.Unix())
	return token, nil
}

// Session finds the user behind a token. A session ends SessionTTL after it
// began however much it is used, as its cookie does, so a stolen token does
// not stay good for as long as someone keeps using it.
func (s *Service) Session(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	h := hashToken(token)
	var userID, expires, seen int64
	err := s.db.QueryRowContext(ctx, "SELECT user_id, expires_at, last_seen_at FROM sessions WHERE token_hash = ?", h).Scan(&userID, &expires, &seen)
	if err != nil {
		return nil, ErrNotFound
	}
	now := time.Now()
	if now.Unix() > expires {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", h)
		return nil, ErrNotFound
	}
	if now.Unix()-seen > 300 {
		_, _ = s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?", now.Unix(), h)
	}
	u, err := s.User(ctx, userID)
	if err != nil || u.Disabled {
		return nil, ErrNotFound
	}
	return u, nil
}

// EndSession signs a session out.
func (s *Service) EndSession(ctx context.Context, token string) {
	_, _ = s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hashToken(token))
}

// ---- helpers ----

// randomDigits returns n random decimal digits.
func randomDigits(n int) string {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, limit)
	if err != nil {
		panic(err) // crypto/rand does not fail
	}
	return fmt.Sprintf("%0*d", n, v)
}

// constantEq compares two secrets in time that doesn't depend on where they differ.
func constantEq(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// isUnique reports whether err is a UNIQUE constraint failing.
func isUnique(err error) bool {
	se, ok := errors.AsType[*sqlite.Error](err)
	return ok && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}
