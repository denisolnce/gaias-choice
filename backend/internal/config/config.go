// Package config loads all runtime settings from the environment, with
// sensible local-dev defaults. Secrets (GitHub PAT, bootstrap credentials)
// have no defaults on purpose.
package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port        string
	DataDir     string
	CORSOrigins []string

	// Debug ⇒ gin runs in debug mode AND the request logger echoes each
	// endpoint's response (the OpenAPI response description + the JSON body,
	// truncated to ResponseLogLines lines). Off ⇒ one access line per request,
	// no body. ResponseLogLines caps that body echo (default 3).
	Debug            bool
	ResponseLogLines int

	// LogExclude is the set of request paths the logger skips entirely (noisy
	// health/poll endpoints). Override via LOG_EXCLUDE (CSV of full paths).
	LogExclude []string

	// Content seam (internal/content). GitHubToken set ⇒ saves proxy the
	// GitHub Contents API (every save is a git commit); LocalContentDir set ⇒
	// saves write that directory instead (dev sandbox) and it TAKES PRECEDENCE
	// so dev edits can never reach the real repo by accident. Neither set ⇒
	// content routes answer 503.
	GitHubToken     string
	GitHubRepo      string
	GitHubBranch    string
	GitHubAPI       string
	LocalContentDir string

	// First-boot admin (internal/auth): created only when the users table is
	// empty and both values are set. Rotating real users happens in the DB,
	// not here.
	BootstrapAdminEmail    string
	BootstrapAdminPassword string

	// Magic-link email (internal/mail). PostmarkToken set ⇒ Postmark's HTTP
	// API (the live transport); else SMTPHost set ⇒ SMTP submission (the
	// provider-neutral fallback); SMTPHost "log" ⇒ emails print to stdout
	// (dev, no creds — wins over everything); nothing set ⇒ /api/auth/magic
	// answers 503. PublicSiteURL is where emailed links point — the SPA
	// consumes #magic=<token> there.
	PostmarkToken  string
	PostmarkStream string
	SMTPHost       string
	SMTPPort       string
	SMTPUser       string
	SMTPPass       string
	MailFrom       string
	PublicSiteURL  string

	// Telegram sign-in (internal/telegram). Set ⇒ the bot long-polls for
	// /start <code> taps and /api/auth/telegram* work; unset ⇒ those endpoints
	// answer 503 and the FE hides the option. The bot's @username is resolved
	// at boot via getMe, never configured here.
	TelegramBotToken string

	// Template enrichment (internal/enrich). AnthropicKey set ⇒ the draft
	// composer's POST /content/template re-tunes a template's prompts to the
	// post title via the Anthropic API; unset ⇒ that endpoint answers 503 and
	// the FE silently keeps the static template. Model defaults when empty.
	AnthropicKey   string
	AnthropicModel string
}

func Load() Config {
	return Config{
		Port:    envOr("PORT", "8787"),
		DataDir: envOr("DATA_DIR", "./data"),
		CORSOrigins: splitCSV(envOr(
			"CORS_ORIGINS",
			"http://localhost:5173,https://denisolnce.github.io",
		)),
		Debug:            envBool("DEBUG"),
		ResponseLogLines: envInt("RESPONSE_LOG_LINES", 3),
		LogExclude: splitCSV(envOr(
			"LOG_EXCLUDE",
			"/api/healthz,/api/auth/me,/api/auth/telegram/poll",
		)),
		GitHubToken:            os.Getenv("GITHUB_TOKEN"),
		GitHubRepo:             envOr("GITHUB_REPO", "denisolnce/gaias-choice"),
		GitHubBranch:           envOr("GITHUB_BRANCH", "main"),
		GitHubAPI:              envOr("GITHUB_API", "https://api.github.com"),
		LocalContentDir:        os.Getenv("LOCAL_CONTENT_DIR"),
		BootstrapAdminEmail:    os.Getenv("BOOTSTRAP_ADMIN_EMAIL"),
		BootstrapAdminPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
		PostmarkToken:          os.Getenv("POSTMARK_TOKEN"),
		PostmarkStream:         envOr("POSTMARK_STREAM", "outbound"),
		SMTPHost:               os.Getenv("SMTP_HOST"),
		SMTPPort:               envOr("SMTP_PORT", "587"),
		SMTPUser:               os.Getenv("SMTP_USER"),
		SMTPPass:               os.Getenv("SMTP_PASS"),
		MailFrom:               envOr("MAIL_FROM", "Gaia's Choice <login@gardenofatlantis.com>"),
		PublicSiteURL: strings.TrimRight(
			envOr("PUBLIC_SITE_URL", "https://denisolnce.github.io/gaias-choice"), "/"),
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		AnthropicKey:     os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:   os.Getenv("ANTHROPIC_MODEL"),
	}
}

func envBool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envInt(key string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return fallback
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
