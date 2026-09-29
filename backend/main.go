// The backend entrypoint: load config, open the store, wire the services,
// serve. Everything else lives in internal/ — config, store (SQLite),
// auth (users/sessions/roles), content (the live-edit seam), httpapi (the
// OpenAPI-contract HTTP layer).
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/denisolnce/gaias-choice/backend/internal/auth"
	"github.com/denisolnce/gaias-choice/backend/internal/config"
	"github.com/denisolnce/gaias-choice/backend/internal/content"
	"github.com/denisolnce/gaias-choice/backend/internal/enrich"
	"github.com/denisolnce/gaias-choice/backend/internal/httpapi"
	"github.com/denisolnce/gaias-choice/backend/internal/mail"
	"github.com/denisolnce/gaias-choice/backend/internal/store"
	"github.com/denisolnce/gaias-choice/backend/internal/telegram"
)

func main() {
	// `/server healthcheck` — the container HEALTHCHECK probe. Distroless has no
	// shell/curl, so the binary probes itself: GET /api/healthz, exit 0 on 200
	// else 1. Docker's unhealthy status then drives doco-cd reconciliation.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	cfg := config.Load()

	if cfg.Debug {
		gin.SetMode(gin.DebugMode)
		log.Printf("DEBUG on: gin debug mode + per-endpoint response logging (%d body lines)", cfg.ResponseLogLines)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	authSvc := auth.New(st)
	created, err := authSvc.Bootstrap(cfg.BootstrapAdminEmail, cfg.BootstrapAdminPassword)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	if created {
		log.Printf("auth: bootstrap admin %q created", cfg.BootstrapAdminEmail)
	}

	mailer := mail.New(mail.Config{
		PostmarkToken:  cfg.PostmarkToken,
		PostmarkStream: cfg.PostmarkStream,
		SMTPHost:       cfg.SMTPHost,
		SMTPPort:       cfg.SMTPPort,
		SMTPUser:       cfg.SMTPUser,
		SMTPPass:       cfg.SMTPPass,
		From:           cfg.MailFrom,
	})
	if mailer == nil {
		log.Print("mail: DISABLED (no POSTMARK_TOKEN or SMTP_HOST) — /api/auth/magic → 503")
	} else {
		log.Printf("mail: %s (from %s)", mailer.Transport(), cfg.MailFrom)
	}

	var bot *telegram.Bot
	if cfg.LocalContentDir != "" {
		// Only one process may long-poll getUpdates for a given bot token
		// (see internal/telegram doc comment); the live VM instance already
		// owns it, so a local run with the same token 409s forever. Local
		// dev is LOCAL_CONTENT_DIR's existing signal — reuse it rather than
		// polling here too.
		log.Print("telegram: DISABLED locally (LOCAL_CONTENT_DIR set) — /api/auth/telegram* → 503")
	} else if b := telegram.New(cfg.TelegramBotToken); b == nil {
		log.Print("telegram: DISABLED (no TELEGRAM_BOT_TOKEN) — /api/auth/telegram* → 503")
	} else if err := b.Init(context.Background()); err != nil {
		log.Printf("telegram: DISABLED — getMe failed: %v", err)
	} else {
		bot = b
		log.Printf("telegram: sign-in bot @%s ready", bot.Username())
		// Long-poll loop owns the bot's getUpdates; runs for the process's life.
		go bot.Run(context.Background(), func(code, username string, tgID int64, name string) string {
			if err := authSvc.ConfirmTelegram(code, username, tgID, name); err != nil {
				if errors.Is(err, auth.ErrBadCredentials) {
					return "This sign-in link is invalid, expired, or for a different account. Start again from the website."
				}
				return "Something went wrong — please try again from the website."
			}
			return "✅ You're signed in. Head back to your browser tab."
		})
	}

	enricher := enrich.New(cfg.AnthropicKey, cfg.AnthropicModel)
	if enricher == nil {
		log.Print("enrich: DISABLED (no ANTHROPIC_API_KEY) — /api/content/template → 503")
	} else {
		log.Print("enrich: template enrichment ready")
	}

	r := httpapi.NewRouter(httpapi.Deps{
		CORSOrigins: cfg.CORSOrigins,
		Store:       st,
		Auth:        authSvc,
		Content:     contentStoreFor(cfg),
		Mailer:      mailer,
		Telegram:    bot,
		Enrich:      enricher,
		SiteURL:     cfg.PublicSiteURL,
		Debug:       cfg.Debug,
		LogLines:    cfg.ResponseLogLines,
		LogExclude:  cfg.LogExclude,
		Descriptions: func() map[string]string {
			if cfg.Debug {
				return endpointDescriptions()
			}
			return nil
		}(),
	})

	log.Printf("listening on :%s (data dir %s)", cfg.Port, cfg.DataDir)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("run: %v", err)
	}
}

// healthcheck probes the running server's readiness endpoint and returns a
// process exit code (0 healthy, 1 not) for Docker's HEALTHCHECK. Same PORT
// default as config.Load, read directly to keep the probe dependency-free.
func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/api/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// contentStoreFor picks the live-edit storage backend and logs the decision —
// the usual "why won't editing turn on" answer is right here at boot. Local
// mode wins when set, even if a GITHUB_TOKEN also happens to be present:
// dev edits must never reach the real repo by accident.
func contentStoreFor(cfg config.Config) content.Store {
	switch {
	case cfg.LocalContentDir != "":
		log.Printf("content seam: LOCAL filesystem at %s — saves write the working tree, no commit/deploy", cfg.LocalContentDir)
		return content.NewLocalStore(cfg.LocalContentDir)
	case cfg.GitHubToken != "":
		log.Printf("content seam: GitHub %s@%s — saves commit and deploy", cfg.GitHubRepo, cfg.GitHubBranch)
		return content.NewGitHubStore(cfg.GitHubToken, cfg.GitHubRepo, cfg.GitHubBranch, cfg.GitHubAPI)
	default:
		log.Print("content seam: DISABLED (no GITHUB_TOKEN or LOCAL_CONTENT_DIR) — /api/content/* → 503")
		return nil
	}
}
