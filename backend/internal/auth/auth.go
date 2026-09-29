// Package auth implements the portal's login: users with admin/editor/viewer
// roles, argon2id password hashes, and opaque bearer session tokens (30-day
// TTL, stored hashed). Register creates viewer accounts (open
// self-registration, rate-limited at the HTTP layer); the first admin comes
// from Bootstrap; editors are an admin concern (future portal endpoints).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/denisolnce/gaias-choice/backend/internal/store"
)

const sessionTTL = 30 * 24 * time.Hour

// magicTTL bounds a magic-link token: long enough to open an inbox on
// another device, short enough that a leaked email is soon worthless.
const magicTTL = 15 * time.Minute

// ErrBadCredentials covers unknown email and wrong password alike — callers
// (and users) never learn which.
var ErrBadCredentials = errors.New("bad credentials")

// ErrEmailTaken — the address already has an account (register only; login
// deliberately never reveals this).
var ErrEmailTaken = errors.New("email already registered")

// ErrInvalid wraps every registration validation failure (bad email shape,
// weak password, unusable display name) — the HTTP layer maps it to 400.
var ErrInvalid = errors.New("invalid input")

// ErrNotFound — the target user id does not exist (admin edit only).
var ErrNotFound = errors.New("user not found")

type Service struct {
	store *store.Store
	// dummyHash is verified against when the email is unknown, so a login
	// probe costs the same time whether or not the account exists.
	dummyHash string
}

func New(st *store.Store) *Service {
	dummy, err := HashPassword(randomToken())
	if err != nil {
		// crypto/rand failing is unrecoverable anyway.
		panic(fmt.Sprintf("auth: init dummy hash: %v", err))
	}
	return &Service{store: st, dummyHash: dummy}
}

// Bootstrap creates the first admin when the users table is empty and both
// values are set, reporting whether it did. It runs on every boot and is a
// no-op after the first — changing the env later does NOT update the user
// (that's a DB concern).
func (s *Service) Bootstrap(email, password string) (bool, error) {
	if email == "" || password == "" {
		return false, nil
	}
	n, err := s.store.CountUsers()
	if err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	if err := checkPasswordPolicy(password); err != nil {
		return false, fmt.Errorf("bootstrap admin: %w", err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, fmt.Errorf("hash bootstrap password: %w", err)
	}
	addr := normalizeEmail(email)
	// The bootstrap env has no display-name knob; the local-part is a sane
	// default the admin can change once profile editing exists.
	if err := s.store.CreateUser(addr, hash, "admin", emailLocalPart(addr)); err != nil {
		return false, fmt.Errorf("create bootstrap admin: %w", err)
	}
	return true, nil
}

// Session is what a successful login yields; Token is the plaintext the
// client stores and sends — the server keeps only its hash.
type Session struct {
	Token     string
	User      store.User
	ExpiresAt time.Time
}

// Login verifies credentials and issues a session. Wrong email and wrong
// password both return ErrBadCredentials; any other error is infrastructure.
func (s *Service) Login(email, password string) (Session, error) {
	// Opportunistic janitor: keep the sessions table free of expired rows.
	_ = s.store.DeleteExpiredSessions(time.Now())

	user, found, err := s.store.UserByEmail(normalizeEmail(email))
	if err != nil {
		return Session{}, err
	}
	if !found {
		// Burn the same argon2 work as a real check — no timing oracle.
		VerifyPassword(password, s.dummyHash)
		return Session{}, ErrBadCredentials
	}
	if !VerifyPassword(password, user.PasswordHash) {
		return Session{}, ErrBadCredentials
	}
	return s.issueSession(user)
}

// Register creates a viewer account (open self-registration — the caller is
// responsible for rate limiting) and signs it straight in. Validation
// failures wrap ErrInvalid; a known email returns ErrEmailTaken.
func (s *Service) Register(email, password, displayName string) (Session, error) {
	_ = s.store.DeleteExpiredSessions(time.Now())

	addr := normalizeEmail(email)
	if err := checkEmail(addr); err != nil {
		return Session{}, err
	}
	if err := checkPasswordPolicy(password); err != nil {
		return Session{}, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	name := strings.Join(strings.Fields(displayName), " ") // trim + collapse whitespace
	if name == "" || len(name) > 50 {
		return Session{}, fmt.Errorf("%w: display name must be 1–50 characters", ErrInvalid)
	}

	if _, exists, err := s.store.UserByEmail(addr); err != nil {
		return Session{}, err
	} else if exists {
		return Session{}, ErrEmailTaken
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Session{}, err
	}
	if err := s.store.CreateUser(addr, hash, "viewer", name); err != nil {
		// Lost the race with a concurrent register for the same address.
		if strings.Contains(err.Error(), "UNIQUE") {
			return Session{}, ErrEmailTaken
		}
		return Session{}, err
	}
	user, _, err := s.store.UserByEmail(addr)
	if err != nil {
		return Session{}, err
	}
	return s.issueSession(user)
}

// UpdateProfile changes a signed-in user's own display name, email, avatar
// URL, and (if password is non-empty) password. Returns ErrInvalid for bad
// input, ErrEmailTaken if the new email belongs to a different account.
func (s *Service) UpdateProfile(userID int64, displayName, email, avatarURL, password string) (store.User, error) {
	// email is optional: Telegram-only accounts have none. Empty ⇒ stored as
	// NULL (UpdateUser), so it stays distinct under the UNIQUE index.
	addr := normalizeEmail(email)
	if addr != "" {
		if err := checkEmail(addr); err != nil {
			return store.User{}, err
		}
		if existing, exists, err := s.store.UserByEmail(addr); err != nil {
			return store.User{}, err
		} else if exists && existing.ID != userID {
			return store.User{}, ErrEmailTaken
		}
	}
	name := strings.Join(strings.Fields(displayName), " ") // trim + collapse whitespace
	if name == "" || len(name) > 50 {
		return store.User{}, fmt.Errorf("%w: display name must be 1–50 characters", ErrInvalid)
	}
	// Avatars may be a URL or an inlined data: URI (a browser-downscaled image
	// the user uploaded). 200 KB comfortably holds a ~256px WebP thumbnail.
	// ponytail: data URI in the users row (and campfire payload) — fine for a
	// small community; move to an object store + upload endpoint at scale.
	if len(avatarURL) > 200_000 {
		return store.User{}, fmt.Errorf("%w: avatar image too large", ErrInvalid)
	}

	passwordHash := "" // "" ⇒ store.UpdateUser leaves the password unchanged
	if password != "" {
		if err := checkPasswordPolicy(password); err != nil {
			return store.User{}, fmt.Errorf("%w: %s", ErrInvalid, err)
		}
		hash, err := HashPassword(password)
		if err != nil {
			return store.User{}, err
		}
		passwordHash = hash
	}

	if err := s.store.UpdateUser(userID, name, addr, avatarURL, passwordHash); err != nil {
		return store.User{}, err
	}
	user, _, err := s.store.UserByID(userID)
	return user, err
}

// AdminUpdateUser lets an admin edit another account's display name, avatar,
// role, and (only if password is non-empty) password. It never touches the
// target's email. Returns ErrNotFound for an unknown id, ErrInvalid for bad
// input.
// ponytail: this can demote the last admin — or the caller themselves —
// deliberately un-guarded for a tiny, trusted admin team; the upgrade is a
// "refuse to remove the last admin" check when the team grows.
func (s *Service) AdminUpdateUser(id int64, displayName, avatarURL, role, password string) (store.User, error) {
	if _, found, err := s.store.UserByID(id); err != nil {
		return store.User{}, err
	} else if !found {
		return store.User{}, ErrNotFound
	}
	if role != "admin" && role != "editor" && role != "viewer" {
		return store.User{}, fmt.Errorf("%w: bad role", ErrInvalid)
	}
	name := strings.Join(strings.Fields(displayName), " ") // trim + collapse whitespace
	if name == "" || len(name) > 50 {
		return store.User{}, fmt.Errorf("%w: display name must be 1–50 characters", ErrInvalid)
	}
	// Avatars may be a URL or an inlined data: URI (a browser-downscaled image);
	// 200 KB comfortably holds a ~256px WebP thumbnail (same cap as UpdateProfile).
	if len(avatarURL) > 200_000 {
		return store.User{}, fmt.Errorf("%w: avatar image too large", ErrInvalid)
	}

	passwordHash := "" // "" ⇒ store.AdminUpdateUser leaves the password unchanged
	if password != "" {
		if err := checkPasswordPolicy(password); err != nil {
			return store.User{}, fmt.Errorf("%w: %s", ErrInvalid, err)
		}
		hash, err := HashPassword(password)
		if err != nil {
			return store.User{}, err
		}
		passwordHash = hash
	}

	if err := s.store.AdminUpdateUser(id, name, avatarURL, role, passwordHash); err != nil {
		return store.User{}, err
	}
	user, _, err := s.store.UserByID(id)
	return user, err
}

// RequestMagic mints a one-time sign-in token for the address and returns
// the plaintext for the caller to email — this package never sends mail.
// The address needs no account yet: redemption creates one (VerifyMagic).
func (s *Service) RequestMagic(email string) (string, error) {
	addr := normalizeEmail(email)
	if err := checkEmail(addr); err != nil {
		return "", err
	}
	// Same lazy janitor pattern as sessions.
	_ = s.store.DeleteExpiredLoginTokens(time.Now())

	token := randomToken()
	if err := s.store.CreateLoginToken(hashToken(token), addr, time.Now().Add(magicTTL)); err != nil {
		return "", err
	}
	return token, nil
}

// VerifyMagic redeems a magic-link token (single-use, TTL-bound) for a
// session, creating a viewer account on first sign-in — the link arriving IS
// the email verification, so unlike Register there is nothing else to check.
// Accounts born this way have no usable password (empty hash ⇒ VerifyPassword
// is always false); they can set one later via UpdateProfile.
func (s *Service) VerifyMagic(token string) (Session, error) {
	email, ok, err := s.store.ConsumeLoginToken(hashToken(token), time.Now())
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrBadCredentials
	}
	user, found, err := s.store.UserByEmail(email)
	if err != nil {
		return Session{}, err
	}
	if !found {
		if err := s.store.CreateUser(email, "", "viewer", emailLocalPart(email)); err != nil {
			return Session{}, err
		}
		if user, _, err = s.store.UserByEmail(email); err != nil {
			return Session{}, err
		}
	}
	return s.issueSession(user)
}

// telegramTTL bounds a Telegram sign-in handshake: long enough to switch to
// the Telegram app and tap the deep link, short enough to be worthless later.
const telegramTTL = 10 * time.Minute

// RequestTelegram mints a one-time code for a claimed @username and returns
// the plaintext the FE puts in the t.me/<bot>?start=<code> deep link. No
// account exists yet — one is created (keyed by telegram id) only once the
// bot confirms the sender (ConfirmTelegram) and the FE redeems (PollTelegram).
func (s *Service) RequestTelegram(username string) (string, error) {
	name, err := normalizeUsername(username)
	if err != nil {
		return "", err
	}
	_ = s.store.DeleteExpiredTelegramLogins(time.Now())

	code := randomToken()
	if err := s.store.CreateTelegramLogin(hashToken(code), name, time.Now().Add(telegramTTL)); err != nil {
		return "", err
	}
	return code, nil
}

// ConfirmTelegram is the security gate: the bot calls it with the code from a
// /start message plus the SENDER's real @username / id / name. It succeeds
// only when the sender's username matches the one claimed at RequestTelegram —
// an attacker who claims someone else's username taps their own bot and fails
// here. ErrBadCredentials covers unknown/expired code and username mismatch
// alike (the bot's reply never says which).
func (s *Service) ConfirmTelegram(code, senderUsername string, tgID int64, senderName string) error {
	now := time.Now()
	tl, found, err := s.store.TelegramLoginByCode(hashToken(code), now)
	if err != nil {
		return err
	}
	// ponytail: temporary login-failure observability — the reply text can't say
	// which branch fired, so log it here (code prefix only, never the full token).
	if !found {
		log.Printf("telegram: confirm miss — code %s… unknown or expired", codePrefix(code))
		return ErrBadCredentials
	}
	claimed, err := normalizeUsername(senderUsername)
	if err != nil || claimed != tl.Username {
		log.Printf("telegram: confirm miss — code %s… sender @%q ≠ claimed @%q (err=%v)",
			codePrefix(code), senderUsername, tl.Username, err)
		return ErrBadCredentials
	}
	name := strings.Join(strings.Fields(senderName), " ")
	if name == "" || len(name) > 50 {
		name = "@" + tl.Username // always leave a usable display name
	}
	ok, err := s.store.ConfirmTelegramLogin(hashToken(code), tgID, name, now)
	if err != nil {
		return err
	}
	if !ok {
		log.Printf("telegram: confirm miss — code %s… raced (expired or already confirmed)", codePrefix(code))
		return ErrBadCredentials // expired between fetch and update, or already confirmed
	}
	return nil
}

// codePrefix returns a short, non-sensitive handle for a login code — enough to
// correlate a failure with a request in the logs without printing the secret.
func codePrefix(code string) string {
	if len(code) > 8 {
		return code[:8]
	}
	return code
}

// PollTelegram is the FE's poll: (Session, true) once the bot has confirmed
// the sender (find-or-create the account keyed by telegram id, single-use —
// the handshake is consumed); (_, false, nil) while still pending;
// ErrBadCredentials when the code is unknown or expired.
func (s *Service) PollTelegram(code string) (Session, bool, error) {
	tl, found, err := s.store.TelegramLoginByCode(hashToken(code), time.Now())
	if err != nil {
		return Session{}, false, err
	}
	if !found {
		return Session{}, false, ErrBadCredentials
	}
	if !tl.TgID.Valid {
		return Session{}, false, nil // not confirmed yet — keep polling
	}
	// Consume first so a late second poll can't mint a second session.
	// ponytail: not atomic with the find-or-create below, fine on this
	// single-connection SQLite; wrap in a tx if it ever runs concurrent.
	if err := s.store.DeleteTelegramLogin(hashToken(code)); err != nil {
		return Session{}, false, err
	}
	user, err := s.findOrCreateTelegram(tl.TgID.Int64, tl.TgName)
	if err != nil {
		return Session{}, false, err
	}
	sess, err := s.issueSession(user)
	return sess, true, err
}

func (s *Service) findOrCreateTelegram(tgID int64, name string) (store.User, error) {
	user, found, err := s.store.UserByTelegramID(tgID)
	if err != nil {
		return store.User{}, err
	}
	if found {
		return user, nil
	}
	if name == "" {
		name = "traveler"
	}
	if err := s.store.CreateTelegramUser(tgID, name); err != nil {
		return store.User{}, err
	}
	user, _, err = s.store.UserByTelegramID(tgID)
	return user, err
}

// normalizeUsername lowercases and strips a leading @, then enforces
// Telegram's own rule (5–32 chars, [A-Za-z0-9_], must start with a letter) so
// a bogus claim never reaches the DB. Case-insensitive: Telegram usernames
// are.
func normalizeUsername(u string) (string, error) {
	u = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(u), "@")))
	if len(u) < 5 || len(u) > 32 {
		return "", fmt.Errorf("%w: telegram username must be 5–32 characters", ErrInvalid)
	}
	for i, r := range u {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok || (i == 0 && !(r >= 'a' && r <= 'z')) {
			return "", fmt.Errorf("%w: not a telegram username", ErrInvalid)
		}
	}
	return u, nil
}

func (s *Service) issueSession(user store.User) (Session, error) {
	token := randomToken()
	expires := time.Now().Add(sessionTTL)
	if err := s.store.CreateSession(hashToken(token), user.ID, expires); err != nil {
		return Session{}, err
	}
	return Session{Token: token, User: user, ExpiresAt: expires}, nil
}

// Validate resolves a bearer token to its user; false for missing, unknown,
// or expired sessions.
func (s *Service) Validate(token string) (store.User, bool) {
	if token == "" {
		return store.User{}, false
	}
	user, ok, err := s.store.UserBySession(hashToken(token), time.Now())
	if err != nil || !ok {
		return store.User{}, false
	}
	return user, true
}

// Logout revokes the session; revoking a nonexistent one is fine.
func (s *Service) Logout(token string) {
	if token != "" {
		_ = s.store.DeleteSession(hashToken(token))
	}
}

// randomToken returns a 256-bit opaque token, URL-safe base64.
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("auth: crypto/rand: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashToken is what actually lands in the sessions table — sha256 hex — so a
// leaked DB doesn't leak live sessions.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// checkEmail is deliberately shallow — real validation is "the magic link
// arrived" (RequestMagic/VerifyMagic). This only rejects obvious garbage.
func checkEmail(addr string) error {
	at := strings.Index(addr, "@")
	if len(addr) > 254 || at < 1 || at == len(addr)-1 || strings.ContainsAny(addr, " \t\n") {
		return fmt.Errorf("%w: not an email address", ErrInvalid)
	}
	return nil
}

// emailLocalPart is the display-name fallback for accounts that predate
// display names (and the bootstrap admin's default).
func emailLocalPart(addr string) string {
	if at := strings.Index(addr, "@"); at > 0 {
		return addr[:at]
	}
	return addr
}

// DisplayName resolves what to show for a user, falling back for pre-003 rows.
func DisplayName(u store.User) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return emailLocalPart(u.Email)
}
