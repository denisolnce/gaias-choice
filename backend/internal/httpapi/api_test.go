// End-to-end tests through the real router: real store (temp dir), real auth
// (bootstrapped admin + login), the OpenAPI-generated routes, and a stub
// GitHub upstream (httptest) or a temp-dir LocalStore behind the content seam.
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/denisolnce/gaias-choice/backend/internal/auth"
	"github.com/denisolnce/gaias-choice/backend/internal/content"
	"github.com/denisolnce/gaias-choice/backend/internal/store"
)

const (
	testEmail    = "owner@test.dev"
	testPassword = "correct-horse"
)

// testEnv builds a fully-wired router around cs (nil = seam unconfigured)
// and returns it with a live session token for the bootstrapped admin.
func testEnv(t *testing.T, cs content.Store) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := auth.New(st)
	if _, err := a.Bootstrap(testEmail, testPassword); err != nil {
		t.Fatal(err)
	}
	r := NewRouter(Deps{Store: st, Auth: a, Content: cs})

	w := do(r, http.MethodPost, "/api/auth/login", "",
		fmt.Sprintf(`{"email":%q,"password":%q}`, testEmail, testPassword))
	if w.Code != http.StatusOK {
		t.Fatalf("login: got %d: %s", w.Code, w.Body.String())
	}
	var resp struct{ Token string }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Token == "" {
		t.Fatalf("login response: %v %s", err, w.Body.String())
	}
	return r, resp.Token
}

func githubSeam(upstream string) content.Store {
	return content.NewGitHubStore("test-pat", "dennislapchenko/gaias-choice", "main", upstream)
}

func do(r *gin.Engine, method, target, bearer, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPublicEndpoints(t *testing.T) {
	r, _ := testEnv(t, nil)
	if w := do(r, http.MethodGet, "/api/healthz", "", ""); w.Code != http.StatusOK {
		t.Errorf("healthz: got %d", w.Code)
	}
}

// Every operation the spec marks `security: session` refuses missing and
// garbage bearers alike — enforcement comes from the spec via SessionScopes.
func TestSpecDrivenAuth(t *testing.T) {
	r, _ := testEnv(t, nil)
	secured := [][2]string{
		{http.MethodGet, "/api/auth/me"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodGet, "/api/users"},
		{http.MethodGet, "/api/content/file?path=content/locales/en/site.yaml"},
		{http.MethodPost, "/api/content/save"},
		{http.MethodPost, "/api/content/commit"},
		{http.MethodPost, "/api/content/image"},
		{http.MethodDelete, "/api/content/file?path=content/locales/en/site.yaml&sha=x"},
	}
	for _, req := range secured {
		for _, bearer := range []string{"", "garbage-token"} {
			if w := do(r, req[0], req[1], bearer, ""); w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s bearer=%q: got %d, want 401", req[0], req[1], bearer, w.Code)
			}
		}
	}
}

func TestLoginFlow(t *testing.T) {
	r, token := testEnv(t, nil)

	if w := do(r, http.MethodPost, "/api/auth/login", "", `{"email":"owner@test.dev","password":"wrong"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: got %d, want 401", w.Code)
	}
	if w := do(r, http.MethodPost, "/api/auth/login", "", `{"email":"owner@test.dev"}`); w.Code != http.StatusBadRequest {
		t.Errorf("missing password: got %d, want 400", w.Code)
	}

	w := do(r, http.MethodGet, "/api/auth/me", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("me: got %d", w.Code)
	}
	var me struct {
		Email   string
		Role    string
		Editing bool
	}
	json.Unmarshal(w.Body.Bytes(), &me)
	if me.Email != testEmail || me.Role != "admin" || me.Editing {
		t.Errorf("me (seam unconfigured): %+v", me)
	}

	if w := do(r, http.MethodPost, "/api/auth/logout", token, ""); w.Code != http.StatusNoContent {
		t.Errorf("logout: got %d, want 204", w.Code)
	}
	if w := do(r, http.MethodGet, "/api/auth/me", token, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("me after logout: got %d, want 401", w.Code)
	}
}

func TestMeReportsEditing(t *testing.T) {
	r, token := testEnv(t, content.NewLocalStore(t.TempDir()))
	w := do(r, http.MethodGet, "/api/auth/me", token, "")
	var me struct{ Editing bool }
	json.Unmarshal(w.Body.Bytes(), &me)
	if !me.Editing {
		t.Error("editing=false with a configured seam")
	}
}

// registerViewer signs up a fresh viewer account and returns its token.
func registerViewer(t *testing.T, r *gin.Engine, email, name string) string {
	t.Helper()
	w := do(r, http.MethodPost, "/api/auth/register", "",
		fmt.Sprintf(`{"email":%q,"password":"viewer-pass-123","displayName":%q}`, email, name))
	if w.Code != http.StatusOK {
		t.Fatalf("register: got %d: %s", w.Code, w.Body.String())
	}
	var resp struct{ Token, Role, DisplayName string }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Token == "" {
		t.Fatalf("register response: %v %s", err, w.Body.String())
	}
	if resp.Role != "viewer" || resp.DisplayName != name {
		t.Fatalf("register grant: %+v", resp)
	}
	return resp.Token
}

func TestRegisterFlow(t *testing.T) {
	r, _ := testEnv(t, nil)
	token := registerViewer(t, r, "camper@test.dev", "Trail Camper")

	// The new session works.
	w := do(r, http.MethodGet, "/api/auth/me", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("me: got %d", w.Code)
	}
	var me struct {
		Email, Role, DisplayName string
		Editing                  bool
	}
	json.Unmarshal(w.Body.Bytes(), &me)
	if me.Email != "camper@test.dev" || me.Role != "viewer" || me.DisplayName != "Trail Camper" || me.Editing {
		t.Errorf("me after register: %+v", me)
	}

	// Same email again → 409; broken inputs → 400.
	if w := do(r, http.MethodPost, "/api/auth/register", "",
		`{"email":"camper@test.dev","password":"another-pass-123","displayName":"Twin"}`); w.Code != http.StatusConflict {
		t.Errorf("duplicate email: got %d, want 409", w.Code)
	}
	for name, body := range map[string]string{
		"missing name":  `{"email":"x@test.dev","password":"long-enough-pass"}`,
		"weak password": `{"email":"x@test.dev","password":"short","displayName":"X"}`,
		"bad email":     `{"email":"not-an-email","password":"long-enough-pass","displayName":"X"}`,
	} {
		if w := do(r, http.MethodPost, "/api/auth/register", "", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, w.Code)
		}
	}
}

// A viewer is a real session — but the editor-scoped content operations turn
// it away with 403, even with a fully configured storage backend. This is
// the invariant that keeps open registration away from the repo-write PAT.
func TestViewerForbiddenFromContent(t *testing.T) {
	upstreamCalls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
	}))
	defer up.Close()
	r, adminToken := testEnv(t, githubSeam(up.URL))
	viewerToken := registerViewer(t, r, "viewer@test.dev", "Just Looking")

	for _, req := range [][2]string{
		{http.MethodGet, "/api/content/file?path=content/locales/en/site.yaml"},
		{http.MethodPost, "/api/content/save"},
		{http.MethodPost, "/api/content/commit"},
		{http.MethodPost, "/api/content/image"},
		{http.MethodDelete, "/api/content/file?path=content/locales/en/site.yaml&sha=x"},
	} {
		body := ""
		if req[0] == http.MethodPost {
			body = `{"path":"content/x.md","content":"x"}`
		}
		if w := do(r, req[0], req[1], viewerToken, body); w.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s: got %d, want 403", req[0], req[1], w.Code)
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("viewer requests reached the upstream %d times", upstreamCalls)
	}

	// Editing capability is role-aware: seam configured, yet viewer sees false.
	var me struct{ Editing bool }
	json.Unmarshal(do(r, http.MethodGet, "/api/auth/me", viewerToken, "").Body.Bytes(), &me)
	if me.Editing {
		t.Error("viewer me.editing=true with a configured seam")
	}
	json.Unmarshal(do(r, http.MethodGet, "/api/auth/me", adminToken, "").Body.Bytes(), &me)
	if !me.Editing {
		t.Error("admin me.editing=false with a configured seam")
	}
}

func TestUsersList(t *testing.T) {
	r, adminToken := testEnv(t, nil)
	viewerToken := registerViewer(t, r, "second@test.dev", "Second Camper")

	w := do(r, http.MethodGet, "/api/users", viewerToken, "")
	if w.Code != http.StatusOK {
		t.Fatalf("users: got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Users []struct {
			DisplayName, Role, JoinedAt string
			You                         bool
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Users) != 2 {
		t.Fatalf("users: got %d, want 2", len(resp.Users))
	}
	// Oldest first: the bootstrap admin (email local-part fallback), then the
	// viewer — who is `you` on this token. Emails never appear.
	if resp.Users[0].DisplayName != "owner" || resp.Users[0].Role != "admin" || resp.Users[0].You {
		t.Errorf("admin row: %+v", resp.Users[0])
	}
	if resp.Users[1].DisplayName != "Second Camper" || !resp.Users[1].You || resp.Users[1].JoinedAt == "" {
		t.Errorf("viewer row: %+v", resp.Users[1])
	}
	if strings.Contains(w.Body.String(), "@") {
		t.Error("users listing leaks an email address")
	}

	// The admin sees the same circle with `you` on their own row.
	json.Unmarshal(do(r, http.MethodGet, "/api/users", adminToken, "").Body.Bytes(), &resp)
	if !resp.Users[0].You || resp.Users[1].You {
		t.Errorf("admin view you-flags: %+v", resp.Users)
	}
}

func TestAdminUpdateUser(t *testing.T) {
	r, adminToken := testEnv(t, nil)
	viewerToken := registerViewer(t, r, "promote-me@test.dev", "Trail Viewer")

	// Find the viewer's id from the listing.
	var list struct {
		Users []struct {
			Id                int64
			DisplayName, Role string
		}
	}
	json.Unmarshal(do(r, http.MethodGet, "/api/users", adminToken, "").Body.Bytes(), &list)
	var viewerID int64
	for _, u := range list.Users {
		if u.DisplayName == "Trail Viewer" {
			viewerID = u.Id
		}
	}
	if viewerID == 0 {
		t.Fatal("viewer id not found in listing")
	}

	target := fmt.Sprintf("/api/users/%d", viewerID)

	// A viewer may not edit anyone — admin scope 403.
	if w := do(r, http.MethodPut, target, viewerToken, `{"displayName":"Hax","role":"admin"}`); w.Code != http.StatusForbidden {
		t.Errorf("viewer edit: got %d, want 403", w.Code)
	}

	// Admin promotes the viewer to editor and renames them.
	w := do(r, http.MethodPut, target, adminToken, `{"displayName":"Promoted Editor","role":"editor"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("admin update: got %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Id          int64
		DisplayName string
		Role        string
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Id != viewerID || got.DisplayName != "Promoted Editor" || got.Role != "editor" {
		t.Errorf("update result: %+v", got)
	}

	// The role change took — the target can now sign in and its /me shows editor.
	// (email untouched: it can still log in with its original address/password.)
	lw := do(r, http.MethodPost, "/api/auth/login", "",
		`{"email":"promote-me@test.dev","password":"viewer-pass-123"}`)
	if lw.Code != http.StatusOK {
		t.Fatalf("target login after edit: got %d — email may have been clobbered", lw.Code)
	}

	// Bad role → 400; unknown id → 404.
	if w := do(r, http.MethodPut, target, adminToken, `{"displayName":"X","role":"wizard"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad role: got %d, want 400", w.Code)
	}
	if w := do(r, http.MethodPut, "/api/users/999999", adminToken, `{"displayName":"Ghost","role":"viewer"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown id: got %d, want 404", w.Code)
	}
}

func TestRegisterRateLimit(t *testing.T) {
	r, _ := testEnv(t, nil)
	last := 0
	for i := 0; i < 12; i++ {
		last = do(r, http.MethodPost, "/api/auth/register", "",
			fmt.Sprintf(`{"email":"bot%d@test.dev","password":"long-enough-pass","displayName":"Bot"}`, i)).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after hammering register: got %d, want 429", last)
	}
}

func TestLoginRateLimit(t *testing.T) {
	r, _ := testEnv(t, nil)
	body := `{"email":"owner@test.dev","password":"wrong"}`
	last := 0
	// testEnv's own login used attempt 1; hammer well past the limit of 10.
	for i := 0; i < 12; i++ {
		last = do(r, http.MethodPost, "/api/auth/login", "", body).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after hammering: got %d, want 429", last)
	}
}

// Auth passes but no storage backend is configured ⇒ 503 (capability, not auth).
func TestNotConfigured(t *testing.T) {
	r, token := testEnv(t, nil)
	for _, req := range [][2]string{
		{http.MethodGet, "/api/content/file?path=content/locales/en/site.yaml"},
		{http.MethodPost, "/api/content/save"},
		{http.MethodPost, "/api/content/commit"},
		{http.MethodPost, "/api/content/image"},
		{http.MethodDelete, "/api/content/file"},
	} {
		// POST/DELETE carry a body — 503 is checked in-handler, after the strict
		// binder, so an empty body would 400 before we reach the capability check.
		body := ""
		switch req[0] {
		case http.MethodPost:
			body = `{"path":"content/x.md","content":"x"}`
		case http.MethodDelete:
			body = `{"paths":["content/locales/en/site.yaml"]}`
		}
		if w := do(r, req[0], req[1], token, body); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: got %d, want 503", req[0], req[1], w.Code)
		}
	}
}

func TestPathValidation(t *testing.T) {
	upstreamCalls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	r, token := testEnv(t, githubSeam(up.URL))

	bad := []string{
		"",
		"../secret",
		"content/../go.mod",
		"content/../../etc/passwd",
		"/etc/passwd",
		"/content/x.md",
		"content//x.md",
		"content/./x.md",
		"content/x.md/",
		"content\\x.md",
		"docs/readme.md",        // outside content/
		"contentious/x.md",      // prefix trick: not content/
		"content/a/..\x00/b.md", // null byte
	}
	for _, p := range bad {
		w := do(r, http.MethodGet, "/api/content/file?path="+url.QueryEscape(p), token, "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("GET path %q: got %d, want 400", p, w.Code)
		}
		body, _ := json.Marshal(map[string]string{"path": p, "content": "x"})
		w = do(r, http.MethodPost, "/api/content/save", token, string(body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST path %q: got %d, want 400", p, w.Code)
		}
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream was called %d times for invalid paths, want 0", upstreamCalls)
	}
	// The prefix rule is exact: contentious/ must fail, content/ must pass
	// through to the upstream (any upstream response is fine here).
	do(r, http.MethodGet, "/api/content/file?path=content/locales/en/site.yaml", token, "")
	if upstreamCalls != 1 {
		t.Errorf("valid path did not reach upstream (calls=%d)", upstreamCalls)
	}
}

func TestReadFile(t *testing.T) {
	fileText := "name: Gaia's Choice\n# a comment that must survive\nepics:\n  - tag: thing\n"
	var sawPath, sawRef, sawAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawRef = r.URL.Query().Get("ref")
		sawAuth = r.Header.Get("Authorization")
		// GitHub wraps base64 in newlines — reproduce that.
		b64 := base64.StdEncoding.EncodeToString([]byte(fileText))
		wrapped := b64[:10] + "\n" + b64[10:]
		json.NewEncoder(w).Encode(map[string]string{
			"sha": "abc123", "content": wrapped, "encoding": "base64",
		})
	}))
	defer up.Close()
	r, token := testEnv(t, githubSeam(up.URL))

	w := do(r, http.MethodGet, "/api/content/file?path=content/locales/en/site.yaml", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Path, Sha, Content string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Content != fileText {
		t.Errorf("decoded content mismatch:\n got: %q\nwant: %q", resp.Content, fileText)
	}
	if resp.Sha != "abc123" {
		t.Errorf("sha: got %q", resp.Sha)
	}
	if sawPath != "/repos/dennislapchenko/gaias-choice/contents/content/locales/en/site.yaml" {
		t.Errorf("upstream path: %q", sawPath)
	}
	if sawRef != "main" {
		t.Errorf("upstream ref: %q", sawRef)
	}
	if sawAuth != "Bearer test-pat" {
		t.Errorf("upstream auth: %q", sawAuth)
	}
}

func TestReadFileNotFound(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer up.Close()
	r, token := testEnv(t, githubSeam(up.URL))
	w := do(r, http.MethodGet, "/api/content/file?path=content/locales/en/products/nope.md", token, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", w.Code)
	}
}

func TestSaveUpdateAndCreate(t *testing.T) {
	type ghPut struct {
		Message   string            `json:"message"`
		Content   string            `json:"content"`
		Branch    string            `json:"branch"`
		SHA       *string           `json:"sha"`
		Committer map[string]string `json:"committer"`
	}
	var got ghPut
	var sawMethod string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawMethod = r.Method
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"content":{"sha":"newblob"},"commit":{"sha":"commit42"}}`)
	}))
	defer up.Close()
	r, token := testEnv(t, githubSeam(up.URL))

	// Update: sha present, default message.
	body, _ := json.Marshal(map[string]string{
		"path": "content/locales/en/site.yaml", "content": "key: value\n", "sha": "oldsha",
	})
	w := do(r, http.MethodPost, "/api/content/save", token, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("update: got %d: %s", w.Code, w.Body.String())
	}
	if sawMethod != http.MethodPut {
		t.Errorf("upstream method: %s", sawMethod)
	}
	if dec, _ := base64.StdEncoding.DecodeString(got.Content); string(dec) != "key: value\n" {
		t.Errorf("upstream content: %q", dec)
	}
	if got.SHA == nil || *got.SHA != "oldsha" {
		t.Errorf("sha not passed through: %v", got.SHA)
	}
	if got.Message != "content: edit content/locales/en/site.yaml via portal" {
		t.Errorf("default message: %q", got.Message)
	}
	if got.Branch != "main" {
		t.Errorf("branch: %q", got.Branch)
	}
	if got.Committer["name"] == "" {
		t.Error("committer missing")
	}
	var resp struct{ Path, Sha, Commit string }
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Sha != "newblob" || resp.Commit != "commit42" {
		t.Errorf("response mapping: %+v", resp)
	}

	// Create: no sha key at all in the upstream body, custom message.
	got = ghPut{}
	body, _ = json.Marshal(map[string]string{
		"path": "content/locales/en/products/new-thing.md", "content": "---\ntitle: X\n---\n",
		"message": "content: draft new-thing via portal",
	})
	w = do(r, http.MethodPost, "/api/content/save", token, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("create: got %d: %s", w.Code, w.Body.String())
	}
	if got.SHA != nil {
		t.Errorf("create must omit sha, got %v", *got.SHA)
	}
	if got.Message != "content: draft new-thing via portal" {
		t.Errorf("custom message: %q", got.Message)
	}
}

func TestUploadImage(t *testing.T) {
	var got struct {
		Content string  `json:"content"`
		SHA     *string `json:"sha"`
		Message string  `json:"message"`
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"content":{"sha":"blob"},"commit":{"sha":"imgcommit"}}`)
	}))
	defer up.Close()
	r, token := testEnv(t, githubSeam(up.URL))

	raw := []byte("\x00\x01\x02not-really-webp-but-bytes")
	b64 := base64.StdEncoding.EncodeToString(raw)
	body, _ := json.Marshal(map[string]string{"path": "frontend/public/images/hero-abc.webp", "contentBase64": b64})
	w := do(r, http.MethodPost, "/api/content/image", token, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("upload: got %d: %s", w.Code, w.Body.String())
	}
	// The Contents API is base64-native, so what GitHub receives is exactly the
	// base64 we sent (decode→re-encode round-trips), created (no sha).
	if got.Content != b64 {
		t.Errorf("upstream content: %q want %q", got.Content, b64)
	}
	if got.SHA != nil {
		t.Errorf("image create must omit sha, got %v", *got.SHA)
	}
	if got.Message != "content: add image frontend/public/images/hero-abc.webp via portal" {
		t.Errorf("message: %q", got.Message)
	}
	var resp struct{ Path, Commit string }
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Path != "frontend/public/images/hero-abc.webp" || resp.Commit != "imgcommit" {
		t.Errorf("response: %+v", resp)
	}

	// JPEG is allowed too (mobile-WebKit WebP-encode fallback).
	jpg, _ := json.Marshal(map[string]string{"path": "frontend/public/images/hero-abc.jpg", "contentBase64": b64})
	if w := do(r, http.MethodPost, "/api/content/image", token, string(jpg)); w.Code != http.StatusOK {
		t.Errorf("jpg upload: got %d, want 200", w.Code)
	}

	// Path outside public/images or an unsupported type ⇒ 400 (never reaches upstream).
	for _, bad := range []string{"content/x.md", "public/images/x.png", "public/other/x.webp", "frontend/public/images/x.png"} {
		body, _ := json.Marshal(map[string]string{"path": bad, "contentBase64": b64})
		if w := do(r, http.MethodPost, "/api/content/image", token, string(body)); w.Code != http.StatusBadRequest {
			t.Errorf("bad path %q: got %d, want 400", bad, w.Code)
		}
	}
}

func TestSaveConflict(t *testing.T) {
	for _, upstreamCode := range []int{http.StatusConflict, http.StatusUnprocessableEntity} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(upstreamCode)
			fmt.Fprint(w, `{"message":"conflict"}`)
		}))
		r, token := testEnv(t, githubSeam(up.URL))
		body, _ := json.Marshal(map[string]string{
			"path": "content/locales/en/site.yaml", "content": "x: 1\n", "sha": "stale",
		})
		w := do(r, http.MethodPost, "/api/content/save", token, string(body))
		if w.Code != http.StatusConflict {
			t.Errorf("upstream %d: got %d, want 409", upstreamCode, w.Code)
		}
		up.Close()
	}
}

// Deleting a post wipes both locale files in ONE commit via the Git Data API
// (Contents API can't multi-file-commit). The fake serves the ref→commit→tree→
// commit→ref-update flow and asserts a single commit drops both paths.
func TestDeleteContent(t *testing.T) {
	p1 := "content/locales/ru/products/old.md"
	p2 := "content/locales/en/products/old.md"
	var treeBody struct {
		BaseTree string `json:"base_tree"`
		Tree     []struct {
			Path string `json:"path"`
			SHA  any    `json:"sha"`
		} `json:"tree"`
	}
	var commitBody struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}
	commits, patched := 0, false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			b64 := base64.StdEncoding.EncodeToString([]byte("---\ntitle: x\n---\n"))
			json.NewEncoder(w).Encode(map[string]any{"sha": "blob", "content": b64, "encoding": "base64"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/ref/heads/main"):
			fmt.Fprint(w, `{"object":{"sha":"headsha"}}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"):
			fmt.Fprint(w, `{"tree":{"sha":"basetree"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/trees"):
			json.NewDecoder(r.Body).Decode(&treeBody)
			fmt.Fprint(w, `{"sha":"newtree"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/commits"):
			commits++
			json.NewDecoder(r.Body).Decode(&commitBody)
			fmt.Fprint(w, `{"sha":"delcommit"}`)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			patched = true
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected upstream call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer up.Close()
	r, token := testEnv(t, githubSeam(up.URL))

	body, _ := json.Marshal(map[string]any{"paths": []string{p1, p2}})
	w := do(r, http.MethodDelete, "/api/content/file", token, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("delete: got %d: %s", w.Code, w.Body.String())
	}
	if commits != 1 || !patched {
		t.Errorf("want exactly 1 commit + ref update, got commits=%d patched=%v", commits, patched)
	}
	if len(treeBody.Tree) != 2 || treeBody.Tree[0].SHA != nil || treeBody.BaseTree != "basetree" {
		t.Errorf("tree should drop both paths with sha:null off basetree: %+v", treeBody)
	}
	if commitBody.Message != "content: delete "+p1+", "+p2+" via portal" {
		t.Errorf("default message: %q", commitBody.Message)
	}
	if len(commitBody.Parents) != 1 || commitBody.Parents[0] != "headsha" || commitBody.Tree != "newtree" {
		t.Errorf("commit mapping: %+v", commitBody)
	}
	var resp struct {
		Paths  []string
		Commit string
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Paths) != 2 || resp.Commit != "delcommit" {
		t.Errorf("response mapping: %+v", resp)
	}
}

// A batch write lands every file in ONE commit via the Git Data API. The fake
// serves the ref→commit→tree→commit→ref-update flow and asserts a single commit
// carries both files' content.
func TestCommitContent(t *testing.T) {
	var treeBody struct {
		BaseTree string `json:"base_tree"`
		Tree     []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"tree"`
	}
	commits, patched := 0, false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/ref/heads/main"):
			fmt.Fprint(w, `{"object":{"sha":"headsha"}}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"):
			fmt.Fprint(w, `{"tree":{"sha":"basetree"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/trees"):
			json.NewDecoder(r.Body).Decode(&treeBody)
			fmt.Fprint(w, `{"sha":"newtree"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/commits"):
			commits++
			fmt.Fprint(w, `{"sha":"newcommit"}`)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			patched = true
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected upstream call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer up.Close()
	r, token := testEnv(t, githubSeam(up.URL))

	body, _ := json.Marshal(map[string]any{"files": []map[string]string{
		{"path": "content/locales/ru/products/x.md", "content": "ru body"},
		{"path": "content/locales/en/products/x.md", "content": "en body"},
	}})
	w := do(r, http.MethodPost, "/api/content/commit", token, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("commit: got %d: %s", w.Code, w.Body.String())
	}
	if commits != 1 || !patched {
		t.Errorf("want exactly 1 commit + ref update, got commits=%d patched=%v", commits, patched)
	}
	if len(treeBody.Tree) != 2 || treeBody.Tree[0].Content != "ru body" || treeBody.BaseTree != "basetree" {
		t.Errorf("tree should carry both files' content off basetree: %+v", treeBody)
	}
	var resp struct {
		Paths  []string
		Commit string
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Paths) != 2 || resp.Commit != "newcommit" {
		t.Errorf("response mapping: %+v", resp)
	}
}

// When none of the listed paths exist (the existence check 404s), there's
// nothing to commit → 404.
func TestDeleteNotFound(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer up.Close()
	r, token := testEnv(t, githubSeam(up.URL))
	body, _ := json.Marshal(map[string]any{"paths": []string{"content/locales/en/site.yaml"}})
	if w := do(r, http.MethodDelete, "/api/content/file", token, string(body)); w.Code != http.StatusNotFound {
		t.Errorf("all-missing delete: got %d, want 404", w.Code)
	}
}

// The dev sandbox: a LocalStore seam routes saves to the filesystem —
// create/read/update round-trip on disk, with the same 404/409 semantics as
// the GitHub path (no upstream, no commit).
func TestLocalStore(t *testing.T) {
	dir := t.TempDir()
	r, token := testEnv(t, content.NewLocalStore(dir))
	p := "content/locales/en/products/local-thing.md"
	q := "/api/content/file?path=" + url.QueryEscape(p)

	// Missing file → 404 (the create pre-flight's "good case").
	if w := do(r, http.MethodGet, q, token, ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing get: got %d, want 404", w.Code)
	}

	// Create (no sha) → 200, writes the file (and its parent dirs) on disk.
	create, _ := json.Marshal(map[string]string{"path": p, "content": "---\ntitle: Local\n---\n"})
	if w := do(r, http.MethodPost, "/api/content/save", token, string(create)); w.Code != http.StatusOK {
		t.Fatalf("create: got %d: %s", w.Code, w.Body.String())
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
	if err != nil || string(onDisk) != "---\ntitle: Local\n---\n" {
		t.Fatalf("file not written: err=%v content=%q", err, onDisk)
	}

	// Create again over an existing file → 409 (never clobber).
	if w := do(r, http.MethodPost, "/api/content/save", token, string(create)); w.Code != http.StatusConflict {
		t.Errorf("recreate: got %d, want 409", w.Code)
	}

	// Read back → 200 with a sha handle.
	w := do(r, http.MethodGet, q, token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("read: got %d", w.Code)
	}
	var got struct{ Sha, Content string }
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Content != "---\ntitle: Local\n---\n" || got.Sha == "" {
		t.Fatalf("read mismatch: %+v", got)
	}

	// Update with a stale sha → 409.
	stale, _ := json.Marshal(map[string]string{"path": p, "content": "x\n", "sha": "deadbeef"})
	if w := do(r, http.MethodPost, "/api/content/save", token, string(stale)); w.Code != http.StatusConflict {
		t.Errorf("stale update: got %d, want 409", w.Code)
	}

	// Update with the current sha → 200, and disk reflects it.
	fresh, _ := json.Marshal(map[string]string{"path": p, "content": "---\ntitle: Local 2\n---\n", "sha": got.Sha})
	if w := do(r, http.MethodPost, "/api/content/save", token, string(fresh)); w.Code != http.StatusOK {
		t.Fatalf("fresh update: got %d: %s", w.Code, w.Body.String())
	}
	onDisk, _ = os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
	if string(onDisk) != "---\ntitle: Local 2\n---\n" {
		t.Errorf("update not written: %q", onDisk)
	}

	// Delete (batch body, no sha guard) → 200, file gone.
	del, _ := json.Marshal(map[string]any{"paths": []string{p}})
	if w := do(r, http.MethodDelete, "/api/content/file", token, string(del)); w.Code != http.StatusOK {
		t.Fatalf("delete: got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after delete: err=%v", err)
	}

	// Delete again (already gone) → 404.
	if w := do(r, http.MethodDelete, "/api/content/file", token, string(del)); w.Code != http.StatusNotFound {
		t.Errorf("re-delete: got %d, want 404", w.Code)
	}
}

func TestSaveSizeCap(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called for oversized content")
	}))
	defer up.Close()
	r, token := testEnv(t, githubSeam(up.URL))
	body, _ := json.Marshal(map[string]string{
		"path": "content/locales/en/site.yaml", "content": strings.Repeat("a", content.MaxBytes+1),
	})
	w := do(r, http.MethodPost, "/api/content/save", token, string(body))
	// Either our explicit 413 or MaxBytesReader tripping first (400) is fine;
	// both refuse. Assert it is one of the two and not a success.
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 413/400", w.Code)
	}
}
