// Package httpapi is the HTTP layer: the gin router, the middlewares, and
// the handlers implementing the OpenAPI contract. gen.go is generated from
// ../../openapi.yaml (`task be:gen`) — endpoints are born in the spec, and
// server here must implement whatever StrictServerInterface demands.
package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/denisolnce/gaias-choice/backend/internal/auth"
	"github.com/denisolnce/gaias-choice/backend/internal/content"
	"github.com/denisolnce/gaias-choice/backend/internal/enrich"
	"github.com/denisolnce/gaias-choice/backend/internal/mail"
	"github.com/denisolnce/gaias-choice/backend/internal/store"
	"github.com/denisolnce/gaias-choice/backend/internal/telegram"
)

// Deps is everything the HTTP layer needs, wired explicitly by main.go.
type Deps struct {
	CORSOrigins []string
	Store       *store.Store
	Auth        *auth.Service
	Content     content.Store    // nil ⇒ content routes answer 503
	Mailer      *mail.Mailer     // nil ⇒ /auth/magic answers 503
	Telegram    *telegram.Bot    // nil ⇒ /auth/telegram* answer 503
	Enrich      *enrich.Enricher // nil ⇒ /content/template answers 503
	SiteURL     string           // where emailed magic links point (no trailing /)

	// Debug ⇒ the request logger echoes each endpoint's response description +
	// body (see requestLogger). LogLines caps that body echo. Descriptions maps
	// "METHOD /path STATUS" → the OpenAPI response description (may be nil).
	Debug        bool
	LogLines     int
	Descriptions map[string]string
	LogExclude   []string // request paths the logger skips entirely (health/poll)
}

// NewRouter builds the fully-wired gin engine: logging, recovery, CORS, a
// global request-body cap, and the generated routes under /api with
// spec-driven session enforcement.
func NewRouter(d Deps) *gin.Engine {
	r := gin.New()
	// Only the compose-network reverse proxy (Caddy) may set X-Forwarded-For;
	// otherwise a client could spoof its IP past the login rate limit.
	_ = r.SetTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"})
	r.Use(requestLogger(d.Debug, d.LogLines, d.Descriptions, d.LogExclude), gin.Recovery(), corsMiddleware(d.CORSOrigins))
	// One global body cap. Sized for the largest legitimate body: an image
	// upload (base64 inflates the decoded cap ~4/3) plus JSON envelope headroom.
	r.Use(func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, content.MaxImageBytes*2)
	})
	// Count every matched request into the day-bucketed traffic table by its
	// route template (bounded cardinality; unmatched scanner noise has no
	// FullPath and is skipped). A lost count must never fail a request, so
	// the error is dropped. Feeds /account → Статистика.
	// ponytail: one DB write per request — batch in memory if traffic ever
	// makes SQLite blink.
	r.Use(func(c *gin.Context) {
		c.Next()
		if p := c.FullPath(); p != "" {
			_ = d.Store.CountHit("api", p)
		}
	})

	srv := &server{
		store:        d.Store,
		auth:         d.Auth,
		content:      d.Content,
		mailer:       d.Mailer,
		telegram:     d.Telegram,
		enrich:       d.Enrich,
		siteURL:      d.SiteURL,
		loginLimiter: newRateLimiter(10, time.Minute),
		// Registration is open to the world; a tighter lid keeps a bot from
		// filling the campfire with junk accounts.
		registerLimiter: newRateLimiter(10, time.Hour),
		// Each magic request sends a real email — same lid as registration.
		magicLimiter: newRateLimiter(10, time.Hour),
		// Poll is cheap and the FE hits it every ~2s for minutes — lenient.
		pollLimiter: newRateLimiter(120, time.Minute),
		// A human clicks through pages far slower than this; only a script
		// hammering /track hits the lid.
		trackLimiter: newRateLimiter(60, time.Minute),
	}
	RegisterHandlersWithOptions(r, NewStrictHandler(srv, nil), GinServerOptions{
		BaseURL:     "/api",
		Middlewares: []MiddlewareFunc{sessionAuth(d.Auth)},
		ErrorHandler: func(c *gin.Context, err error, status int) {
			c.JSON(status, Error{Error: err.Error()})
		},
	})
	return r
}

type server struct {
	store           *store.Store
	auth            *auth.Service
	content         content.Store
	mailer          *mail.Mailer
	telegram        *telegram.Bot
	enrich          *enrich.Enricher
	siteURL         string
	loginLimiter    *rateLimiter
	registerLimiter *rateLimiter
	magicLimiter    *rateLimiter
	pollLimiter     *rateLimiter
	trackLimiter    *rateLimiter
}

// Compile-time proof the contract is fully implemented.
var _ StrictServerInterface = (*server)(nil)

// --- system --------------------------------------------------------------------

// GetHealth is the readiness probe the container healthcheck hits: 200 only
// when the DB round-trips, so a wedged-but-alive process (unreachable SQLite,
// stuck WAL) reports unhealthy and doco-cd reconciliation restarts it. A
// returned error maps to 500 (non-200) via the strict handler — that's the
// unhealthy signal; no dedicated 503 in the spec needed.
func (s *server) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	if err := s.store.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db unreachable: %w", err)
	}
	return GetHealth200JSONResponse{Status: "ok"}, nil
}

// --- auth ----------------------------------------------------------------------

func (s *server) Login(ctx context.Context, req LoginRequestObject) (LoginResponseObject, error) {
	if c := ginContext(ctx); c != nil && !s.loginLimiter.allow(c.ClientIP()) {
		return Login429JSONResponse{RateLimitedJSONResponse{Error: "too many attempts — try again later"}}, nil
	}
	if req.Body == nil || req.Body.Email == "" || req.Body.Password == "" {
		return Login400JSONResponse{BadRequestJSONResponse{Error: "email and password required"}}, nil
	}
	sess, err := s.auth.Login(req.Body.Email, req.Body.Password)
	if errors.Is(err, auth.ErrBadCredentials) {
		return Login401JSONResponse{UnauthorizedJSONResponse{Error: "bad credentials"}}, nil
	}
	if err != nil {
		return nil, err // infrastructure — generated code answers 500
	}
	return Login200JSONResponse(grant(sess)), nil
}

func (s *server) Register(ctx context.Context, req RegisterRequestObject) (RegisterResponseObject, error) {
	if c := ginContext(ctx); c != nil && !s.registerLimiter.allow(c.ClientIP()) {
		return Register429JSONResponse{RateLimitedJSONResponse{Error: "too many attempts — try again later"}}, nil
	}
	if req.Body == nil || req.Body.Email == "" || req.Body.Password == "" || req.Body.DisplayName == "" {
		return Register400JSONResponse{BadRequestJSONResponse{Error: "email, password and display name required"}}, nil
	}
	sess, err := s.auth.Register(req.Body.Email, req.Body.Password, req.Body.DisplayName)
	switch {
	case errors.Is(err, auth.ErrEmailTaken):
		return Register409JSONResponse(Error{Error: "email already registered"}), nil
	case errors.Is(err, auth.ErrInvalid):
		return Register400JSONResponse{BadRequestJSONResponse{Error: err.Error()}}, nil
	case err != nil:
		return nil, err
	}
	return Register200JSONResponse(grant(sess)), nil
}

func (s *server) RequestMagicLink(ctx context.Context, req RequestMagicLinkRequestObject) (RequestMagicLinkResponseObject, error) {
	if s.mailer == nil {
		return RequestMagicLink503JSONResponse(Error{Error: "sign-in links not configured"}), nil
	}
	if c := ginContext(ctx); c != nil && !s.magicLimiter.allow(c.ClientIP()) {
		return RequestMagicLink429JSONResponse{RateLimitedJSONResponse{Error: "too many attempts — try again later"}}, nil
	}
	if req.Body == nil || req.Body.Email == "" {
		return RequestMagicLink400JSONResponse{BadRequestJSONResponse{Error: "email required"}}, nil
	}
	token, err := s.auth.RequestMagic(req.Body.Email)
	if errors.Is(err, auth.ErrInvalid) {
		return RequestMagicLink400JSONResponse{BadRequestJSONResponse{Error: "not an email address"}}, nil
	}
	if err != nil {
		return nil, err
	}
	locale := ""
	if req.Body.Locale != nil {
		locale = *req.Body.Locale
	}
	// /magic is a real SPA route (Home reused) so magic-link landings show up
	// as their own path in analytics instead of inflating "/".
	subject, body := magicEmail(locale, s.siteURL+"/magic#magic="+token)
	// Send failure is an ops problem (bad SMTP config, provider down), not
	// the user's: log it, keep the answer uniform.
	if err := s.mailer.Send(strings.TrimSpace(req.Body.Email), subject, body); err != nil {
		log.Printf("mail: magic-link send failed: %v", err)
	}
	return RequestMagicLink200JSONResponse{Status: "sent"}, nil
}

func (s *server) VerifyMagicLink(ctx context.Context, req VerifyMagicLinkRequestObject) (VerifyMagicLinkResponseObject, error) {
	if c := ginContext(ctx); c != nil && !s.loginLimiter.allow(c.ClientIP()) {
		return VerifyMagicLink429JSONResponse{RateLimitedJSONResponse{Error: "too many attempts — try again later"}}, nil
	}
	if req.Body == nil || req.Body.Token == "" {
		return VerifyMagicLink400JSONResponse{BadRequestJSONResponse{Error: "token required"}}, nil
	}
	sess, err := s.auth.VerifyMagic(req.Body.Token)
	if errors.Is(err, auth.ErrBadCredentials) {
		return VerifyMagicLink401JSONResponse(Error{Error: "link invalid, expired, or already used"}), nil
	}
	if err != nil {
		return nil, err
	}
	return VerifyMagicLink200JSONResponse(grant(sess)), nil
}

func (s *server) RequestTelegramLogin(ctx context.Context, req RequestTelegramLoginRequestObject) (RequestTelegramLoginResponseObject, error) {
	if s.telegram == nil {
		return RequestTelegramLogin503JSONResponse(Error{Error: "telegram sign-in not configured"}), nil
	}
	if c := ginContext(ctx); c != nil && !s.loginLimiter.allow(c.ClientIP()) {
		return RequestTelegramLogin429JSONResponse{RateLimitedJSONResponse{Error: "too many attempts — try again later"}}, nil
	}
	if req.Body == nil || req.Body.Username == "" {
		return RequestTelegramLogin400JSONResponse{BadRequestJSONResponse{Error: "username required"}}, nil
	}
	code, err := s.auth.RequestTelegram(req.Body.Username)
	if errors.Is(err, auth.ErrInvalid) {
		return RequestTelegramLogin400JSONResponse{BadRequestJSONResponse{Error: "not a telegram username"}}, nil
	}
	if err != nil {
		return nil, err
	}
	return RequestTelegramLogin200JSONResponse{Code: code, Bot: s.telegram.Username()}, nil
}

func (s *server) PollTelegramLogin(ctx context.Context, req PollTelegramLoginRequestObject) (PollTelegramLoginResponseObject, error) {
	if s.telegram == nil {
		return PollTelegramLogin503JSONResponse(Error{Error: "telegram sign-in not configured"}), nil
	}
	if c := ginContext(ctx); c != nil && !s.pollLimiter.allow(c.ClientIP()) {
		return PollTelegramLogin429JSONResponse{RateLimitedJSONResponse{Error: "too many attempts — try again later"}}, nil
	}
	if req.Body == nil || req.Body.Code == "" {
		return PollTelegramLogin400JSONResponse{BadRequestJSONResponse{Error: "code required"}}, nil
	}
	sess, granted, err := s.auth.PollTelegram(req.Body.Code)
	if errors.Is(err, auth.ErrBadCredentials) {
		return PollTelegramLogin401JSONResponse(Error{Error: "code unknown or expired"}), nil
	}
	if err != nil {
		return nil, err
	}
	if !granted {
		return PollTelegramLogin202JSONResponse{Status: "pending"}, nil
	}
	return PollTelegramLogin200JSONResponse(grant(sess)), nil
}

// magicEmail is the whole email-template system: two locales, plain text.
func magicEmail(locale, link string) (subject, body string) {
	if strings.HasPrefix(locale, "ru") {
		return "Вход на Gaia's Choice",
			"Здравствуйте!\n\nЧтобы войти, откройте ссылку:\n\n" + link +
				"\n\nОна сработает один раз и действует 15 минут. Если вы не запрашивали вход — просто проигнорируйте это письмо.\n\n— Gaia's Choice"
	}
	return "Sign in to Gaia's Choice",
		"Hello!\n\nOpen this link to sign in:\n\n" + link +
			"\n\nIt works once and expires in 15 minutes. If you didn't request it, just ignore this email.\n\n— Gaia's Choice"
}

// grant maps an issued session onto the shared login/register response body.
func grant(sess auth.Session) SessionGrant {
	return SessionGrant{
		Token:       sess.Token,
		Role:        Role(sess.User.Role),
		DisplayName: auth.DisplayName(sess.User),
		ExpiresAt:   sess.ExpiresAt,
	}
}

func (s *server) Logout(ctx context.Context, _ LogoutRequestObject) (LogoutResponseObject, error) {
	if c := ginContext(ctx); c != nil {
		s.auth.Logout(bearerToken(c))
	}
	return Logout204Response{}, nil
}

func (s *server) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	u, ok := sessionUser(ctx)
	if !ok { // unreachable behind sessionAuth; belt and braces
		return GetMe401JSONResponse{UnauthorizedJSONResponse{Error: "unauthorized"}}, nil
	}
	return GetMe200JSONResponse(meResponse(s, u)), nil
}

// meResponse is the shared GetMe/UpdateMe body.
func meResponse(s *server, u store.User) MeResponse {
	return MeResponse{
		Email:       u.Email,
		Role:        Role(u.Role),
		DisplayName: auth.DisplayName(u),
		AvatarUrl:   u.AvatarURL,
		// Edit chrome needs both a configured storage backend and a role
		// that the content endpoints will actually let through.
		Editing: s.content != nil && u.CanEdit(),
	}
}

func (s *server) UpdateMe(ctx context.Context, req UpdateMeRequestObject) (UpdateMeResponseObject, error) {
	caller, ok := sessionUser(ctx)
	if !ok { // unreachable behind sessionAuth; belt and braces
		return UpdateMe401JSONResponse{UnauthorizedJSONResponse{Error: "unauthorized"}}, nil
	}
	if req.Body == nil || req.Body.DisplayName == "" {
		return UpdateMe400JSONResponse{BadRequestJSONResponse{Error: "display name required"}}, nil
	}
	email := "" // empty ⇒ Telegram-only account, no email (UpdateProfile stores NULL)
	if req.Body.Email != nil {
		email = *req.Body.Email
	}
	avatarURL := ""
	if req.Body.AvatarUrl != nil {
		avatarURL = *req.Body.AvatarUrl
	}
	password := ""
	if req.Body.Password != nil {
		password = *req.Body.Password
	}
	updated, err := s.auth.UpdateProfile(caller.ID, req.Body.DisplayName, email, avatarURL, password)
	switch {
	case errors.Is(err, auth.ErrEmailTaken):
		return UpdateMe409JSONResponse(Error{Error: "email already registered"}), nil
	case errors.Is(err, auth.ErrInvalid):
		return UpdateMe400JSONResponse{BadRequestJSONResponse{Error: err.Error()}}, nil
	case err != nil:
		return nil, err
	}
	return UpdateMe200JSONResponse(meResponse(s, updated)), nil
}

func (s *server) ListUsers(ctx context.Context, _ ListUsersRequestObject) (ListUsersResponseObject, error) {
	caller, ok := sessionUser(ctx)
	if !ok { // unreachable behind sessionAuth; belt and braces
		return ListUsers401JSONResponse{UnauthorizedJSONResponse{Error: "unauthorized"}}, nil
	}
	members, err := s.store.ListMembers()
	if err != nil {
		return nil, err
	}
	resp := ListUsers200JSONResponse{}
	for _, m := range members {
		name := m.DisplayName
		if name == "" { // pre-003 account
			name = auth.DisplayName(store.User{Email: m.Email})
		}
		resp.Users = append(resp.Users, struct {
			AvatarUrl   *string            `json:"avatarUrl,omitempty"`
			DisplayName string             `json:"displayName"`
			Id          int64              `json:"id"`
			JoinedAt    openapi_types.Date `json:"joinedAt"`
			Role        Role               `json:"role"`
			You         bool               `json:"you"`
		}{
			AvatarUrl:   &m.AvatarURL,
			DisplayName: name,
			Id:          m.ID,
			JoinedAt:    openapi_types.Date{Time: m.CreatedAt},
			Role:        Role(m.Role),
			You:         m.ID == caller.ID,
		})
	}
	return resp, nil
}

// UpdateUser is the admin-only "edit another user" endpoint. The admin scope
// is enforced in sessionAuth; the sessionUser guard here is belt-and-braces.
// It edits display name, avatar, role, and (only when provided) password —
// never the target's email.
func (s *server) UpdateUser(ctx context.Context, req UpdateUserRequestObject) (UpdateUserResponseObject, error) {
	if _, ok := sessionUser(ctx); !ok { // unreachable behind sessionAuth; belt and braces
		return UpdateUser401JSONResponse{UnauthorizedJSONResponse{Error: "unauthorized"}}, nil
	}
	if req.Body == nil || req.Body.DisplayName == "" {
		return UpdateUser400JSONResponse{BadRequestJSONResponse{Error: "display name and role required"}}, nil
	}
	avatarURL := ""
	if req.Body.AvatarUrl != nil {
		avatarURL = *req.Body.AvatarUrl
	}
	password := ""
	if req.Body.Password != nil {
		password = *req.Body.Password
	}
	u, err := s.auth.AdminUpdateUser(req.Id, req.Body.DisplayName, avatarURL, string(req.Body.Role), password)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return UpdateUser404JSONResponse(Error{Error: "user not found"}), nil
	case errors.Is(err, auth.ErrInvalid):
		return UpdateUser400JSONResponse{BadRequestJSONResponse{Error: err.Error()}}, nil
	case err != nil:
		return nil, err
	}
	return UpdateUser200JSONResponse{
		Id:          u.ID,
		DisplayName: auth.DisplayName(u),
		AvatarUrl:   &u.AvatarURL,
		Role:        Role(u.Role),
	}, nil
}

// --- content -------------------------------------------------------------------

// commitMsg is the shared commit-message rule for the content endpoints: the
// client's message when given, else the fallback, capped at 200 chars.
func commitMsg(p *string, fallback string) string {
	msg := fallback
	if p != nil && *p != "" {
		msg = *p
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

func (s *server) GetContentFile(_ context.Context, req GetContentFileRequestObject) (GetContentFileResponseObject, error) {
	if s.content == nil {
		return GetContentFile503JSONResponse{NotConfiguredJSONResponse{Error: "editing not configured"}}, nil
	}
	p := req.Params.Path
	if !content.ValidPath(p) {
		return GetContentFile400JSONResponse{BadRequestJSONResponse{Error: "invalid path"}}, nil
	}
	res, err := s.content.Get(p)
	if err != nil {
		return GetContentFile502JSONResponse{UpstreamJSONResponse{Error: err.Error()}}, nil
	}
	if res.NotFound {
		return GetContentFile404JSONResponse(Error{Error: "not found"}), nil
	}
	return GetContentFile200JSONResponse{Path: p, Sha: res.SHA, Content: res.Content}, nil
}

func (s *server) SaveContent(_ context.Context, req SaveContentRequestObject) (SaveContentResponseObject, error) {
	if s.content == nil {
		return SaveContent503JSONResponse{NotConfiguredJSONResponse{Error: "editing not configured"}}, nil
	}
	if req.Body == nil {
		return SaveContent400JSONResponse{BadRequestJSONResponse{Error: "bad request body"}}, nil
	}
	b := req.Body
	if !content.ValidPath(b.Path) {
		return SaveContent400JSONResponse{BadRequestJSONResponse{Error: "invalid path"}}, nil
	}
	if len(b.Content) == 0 || len(b.Content) > content.MaxBytes {
		return SaveContent413JSONResponse(Error{Error: "content empty or too large"}), nil
	}
	msg := commitMsg(b.Message, fmt.Sprintf("content: edit %s via portal", b.Path))
	sha := ""
	if b.Sha != nil {
		sha = *b.Sha
	}
	res, err := s.content.Save(b.Path, b.Content, sha, msg)
	if err != nil {
		return SaveContent502JSONResponse{UpstreamJSONResponse{Error: err.Error()}}, nil
	}
	if res.Conflict {
		return SaveContent409JSONResponse(Error{Error: "conflict — file changed"}), nil
	}
	return SaveContent200JSONResponse{Path: b.Path, Sha: res.SHA, Commit: res.Commit}, nil
}

func (s *server) DeleteContent(_ context.Context, req DeleteContentRequestObject) (DeleteContentResponseObject, error) {
	if s.content == nil {
		return DeleteContent503JSONResponse{NotConfiguredJSONResponse{Error: "editing not configured"}}, nil
	}
	if req.Body == nil || len(req.Body.Paths) == 0 {
		return DeleteContent400JSONResponse{BadRequestJSONResponse{Error: "no paths"}}, nil
	}
	for _, p := range req.Body.Paths {
		// content/ files (posts) or frontend/public/images/*.webp (picker delete).
		if !content.ValidPath(p) && !content.ValidImagePath(p) {
			return DeleteContent400JSONResponse{BadRequestJSONResponse{Error: "invalid path"}}, nil
		}
	}
	msg := commitMsg(req.Body.Message, fmt.Sprintf("content: delete %s via portal", strings.Join(req.Body.Paths, ", ")))
	res, err := s.content.Delete(req.Body.Paths, msg)
	if err != nil {
		return DeleteContent502JSONResponse{UpstreamJSONResponse{Error: err.Error()}}, nil
	}
	if res.NotFound {
		return DeleteContent404JSONResponse(Error{Error: "not found"}), nil
	}
	return DeleteContent200JSONResponse{Paths: res.Deleted, Commit: res.Commit}, nil
}

// CommitContent writes several files in one commit (a post's ru+en files
// together). Same validation as Save, per file; no sha guard.
func (s *server) CommitContent(_ context.Context, req CommitContentRequestObject) (CommitContentResponseObject, error) {
	if s.content == nil {
		return CommitContent503JSONResponse{NotConfiguredJSONResponse{Error: "editing not configured"}}, nil
	}
	if req.Body == nil || len(req.Body.Files) == 0 {
		return CommitContent400JSONResponse{BadRequestJSONResponse{Error: "no files"}}, nil
	}
	files := make([]content.FileWrite, 0, len(req.Body.Files))
	paths := make([]string, 0, len(req.Body.Files))
	for _, f := range req.Body.Files {
		if !content.ValidPath(f.Path) {
			return CommitContent400JSONResponse{BadRequestJSONResponse{Error: "invalid path"}}, nil
		}
		if len(f.Content) == 0 || len(f.Content) > content.MaxBytes {
			return CommitContent400JSONResponse{BadRequestJSONResponse{Error: "content empty or too large"}}, nil
		}
		files = append(files, content.FileWrite{Path: f.Path, Content: f.Content})
		paths = append(paths, f.Path)
	}
	msg := commitMsg(req.Body.Message, fmt.Sprintf("content: update %s via portal", strings.Join(paths, ", ")))
	res, err := s.content.SaveMany(files, msg)
	if err != nil {
		return CommitContent502JSONResponse{UpstreamJSONResponse{Error: err.Error()}}, nil
	}
	return CommitContent200JSONResponse{Paths: res.Paths, Commit: res.Commit}, nil
}

// UploadImage commits one browser-downscaled WebP into frontend/public/images/ as its
// own commit, so a post can reference it by /images/<name>. Reuses the plain
// single-file Save (the Contents API is base64-native) — create-only, so a
// name collision surfaces as 409.
func (s *server) UploadImage(_ context.Context, req UploadImageRequestObject) (UploadImageResponseObject, error) {
	if s.content == nil {
		return UploadImage503JSONResponse{NotConfiguredJSONResponse{Error: "editing not configured"}}, nil
	}
	if req.Body == nil || req.Body.Path == "" || req.Body.ContentBase64 == "" {
		return UploadImage400JSONResponse{BadRequestJSONResponse{Error: "path and image required"}}, nil
	}
	if !content.ValidImagePath(req.Body.Path) {
		return UploadImage400JSONResponse{BadRequestJSONResponse{Error: "invalid image path"}}, nil
	}
	data, err := base64.StdEncoding.DecodeString(req.Body.ContentBase64)
	if err != nil {
		return UploadImage400JSONResponse{BadRequestJSONResponse{Error: "bad base64"}}, nil
	}
	if len(data) == 0 || len(data) > content.MaxImageBytes {
		return UploadImage413JSONResponse(Error{Error: "image empty or too large"}), nil
	}
	res, err := s.content.Save(req.Body.Path, string(data), "", fmt.Sprintf("content: add image %s via portal", req.Body.Path))
	if err != nil {
		return UploadImage502JSONResponse{UpstreamJSONResponse{Error: err.Error()}}, nil
	}
	if res.Conflict {
		return UploadImage409JSONResponse(Error{Error: "an image already exists at that path"}), nil
	}
	return UploadImage200JSONResponse{Path: req.Body.Path, Commit: res.Commit}, nil
}

// EnrichTemplate re-tunes a blank template's prompts to a post title via the
// Anthropic API (internal/enrich). It reshapes questions only — never authors
// content — so the provenance contract holds. Nil enricher ⇒ 503 and the FE
// silently keeps the static template.
func (s *server) EnrichTemplate(ctx context.Context, req EnrichTemplateRequestObject) (EnrichTemplateResponseObject, error) {
	if s.enrich == nil {
		return EnrichTemplate503JSONResponse{NotConfiguredJSONResponse{Error: "template enrichment not configured"}}, nil
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Title) == "" || req.Body.Template == "" {
		return EnrichTemplate400JSONResponse{BadRequestJSONResponse{Error: "title and template required"}}, nil
	}
	body, err := s.enrich.Enrich(ctx, req.Body.Title, req.Body.Template)
	if err != nil {
		return EnrichTemplate502JSONResponse{UpstreamJSONResponse{Error: err.Error()}}, nil
	}
	return EnrichTemplate200JSONResponse{Body: body}, nil
}

// languageNames maps our locale codes to the display name the translate prompt
// uses. Unknown codes fall through to the code itself (the model copes).
var languageNames = map[string]string{"en": "English", "ru": "Russian"}

// TranslateContent translates a whole content file's prose into another locale
// via the Anthropic API (internal/enrich). It's a faithful translation of
// existing human-written content, disclosed on the sibling file by the FE's
// `translatedFrom:` mark — so it stays inside the provenance contract. Nil
// enricher ⇒ 503.
func (s *server) TranslateContent(ctx context.Context, req TranslateContentRequestObject) (TranslateContentResponseObject, error) {
	if s.enrich == nil {
		return TranslateContent503JSONResponse{NotConfiguredJSONResponse{Error: "translation not configured"}}, nil
	}
	if req.Body == nil || req.Body.Text == "" || strings.TrimSpace(req.Body.TargetLocale) == "" {
		return TranslateContent400JSONResponse{BadRequestJSONResponse{Error: "text and targetLocale required"}}, nil
	}
	lang := languageNames[req.Body.TargetLocale]
	if lang == "" {
		lang = req.Body.TargetLocale
	}
	text, err := s.enrich.Translate(ctx, lang, req.Body.Text)
	if err != nil {
		return TranslateContent502JSONResponse{UpstreamJSONResponse{Error: err.Error()}}, nil
	}
	return TranslateContent200JSONResponse{Text: text}, nil
}
