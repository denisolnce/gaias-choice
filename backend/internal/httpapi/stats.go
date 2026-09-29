package httpapi

import (
	"context"
	"strings"
	"time"

	"github.com/denisolnce/gaias-choice/backend/internal/store"
)

// pageRoutes mirrors the SPA route roots in frontend/src/App.tsx — the only
// paths /track counts as themselves. Anything else becomes "(other)", so a
// junk flood can't grow the traffic table past real-paths × days.
var pageRoutes = []string{
	"/reviews", "/journal", "/compass", "/about", "/contact",
	"/roadmap", "/disclosure", "/privacy", "/support", "/account", "/magic",
}

// normalizePagePath reduces a reported pathname to something safe to store:
// "/" or a known route root (optionally with "/<slug>"), clean charset, no
// "..", ≤128 chars, trailing slash stripped — else the shared "(other)"
// bucket. /track is public, so this is a trust boundary: the return value is
// the ONLY client-derived string that reaches the DB.
func normalizePagePath(p string) string {
	p = strings.TrimSpace(p)
	if q := strings.IndexAny(p, "?#"); q >= 0 { // defensive; the FE sends pathname only
		p = p[:q]
	}
	if p != "/" {
		p = strings.TrimSuffix(p, "/")
	}
	if p == "/" {
		return p
	}
	if len(p) > 128 || !strings.HasPrefix(p, "/") || strings.Contains(p, "..") || !cleanPath(p) {
		return "(other)"
	}
	for _, r := range pageRoutes {
		if p == r || strings.HasPrefix(p, r+"/") {
			return p
		}
	}
	return "(other)"
}

func cleanPath(p string) bool {
	for _, ch := range p {
		ok := ch == '/' || ch == '-' || ch == '_' || ch == '.' ||
			(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// TrackPageview is the cookieless pageview counter: one day-bucketed hit,
// nothing about the visitor read or stored (the /privacy page's promise).
func (s *server) TrackPageview(ctx context.Context, req TrackPageviewRequestObject) (TrackPageviewResponseObject, error) {
	if c := ginContext(ctx); c != nil && !s.trackLimiter.allow(c.ClientIP()) {
		return TrackPageview429JSONResponse{RateLimitedJSONResponse{Error: "too many requests — slow down"}}, nil
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Path) == "" {
		return TrackPageview400JSONResponse{BadRequestJSONResponse{Error: "path required"}}, nil
	}
	if err := s.store.CountHit("page", normalizePagePath(req.Body.Path)); err != nil {
		return nil, err
	}
	return TrackPageview204Response{}, nil
}

// GetStats feeds the campfire's admin Статистика view: page + API hit totals
// summed over a window. Admin scope enforced by the spec (session: [admin]).
func (s *server) GetStats(_ context.Context, req GetStatsRequestObject) (GetStatsResponseObject, error) {
	rng := N7d
	if req.Params.Range != nil && req.Params.Range.Valid() {
		rng = *req.Params.Range
	}
	// alltime = no date floor; "" sorts before any YYYY-MM-DD, so day >= "" keeps every row.
	since := ""
	if rng != Alltime {
		days := 7
		switch rng {
		case Today:
			days = 1
		case N30d:
			days = 30
		}
		since = time.Now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	}
	pages, err := s.store.Traffic("page", since)
	if err != nil {
		return nil, err
	}
	api, err := s.store.Traffic("api", since)
	if err != nil {
		return nil, err
	}
	return GetStats200JSONResponse{Range: string(rng), Pages: toPathHits(pages), Api: toPathHits(api)}, nil
}

// toPathHits maps store rows onto the generated wire type. Always non-nil so
// an empty window serializes as [] rather than null.
func toPathHits(rows []store.PathHits) []PathHits {
	out := make([]PathHits, len(rows))
	for i, r := range rows {
		out[i] = PathHits{Path: r.Path, Hits: r.Hits}
	}
	return out
}
