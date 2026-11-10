package api

import (
	"net/http"

	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/api/middleware"
	"github.com/Herenn/Infralens/backend/mcp"
)

// MCP endpoint wiring.
//
// /mcp is served AHEAD of the global middleware chain rather than through it,
// on an exact-path match ("/mcp", and "/mcp/" for clients that add a slash):
//
//   - Auth: it enforces its own policy (mcp.KeyAuthenticator, see
//     newMCPEndpoint), which accepts the read-only MCP_API_KEY as well as
//     API_KEY. MCP_API_KEY is valid here and nowhere else, because only this
//     branch knows about it. The UI session (middleware.UIAuth) is in the global chain and never sees
//     /mcp, so cookies cannot authenticate it by construction. This is the
//     only bypass of the global auth: UIAuth keeps no list of exempt
//     "self-authenticating" paths.
//   - CORS: the global rs/cors layer would answer preflights itself, with a
//     header list that lacks the MCP headers and an origin list
//     (CORS_ORIGINS) that disagrees with MCP_ALLOWED_ORIGINS. The MCP handler
//     owns one coherent cross-origin policy instead.
//
// Recovery, metrics, logging and the body limit still apply.

// newMCPEndpoint builds the /mcp handler from config.
//
// Access policy, in order:
//
//   - a key configured (MCP_API_KEY and/or API_KEY): one of them is required;
//   - no key, UI_PASSWORD set: refused. The UI is behind a login, and an open
//     /mcp would serve the same topology to anyone who can reach the server;
//     ALLOW_NO_AUTH does not change that (it is about agent ingest, not about
//     reading the topology past the login);
//   - no key, no UI_PASSWORD: open only with ALLOW_NO_AUTH, as in v3.0 for
//     every other read endpoint.
//
// The session cookie never authenticates /mcp (see mcp.Authenticator).
func (s *Server) newMCPEndpoint() http.Handler {
	keys := []string{s.config.MCP.APIKey, s.config.Auth.APIKey}
	anyKey := s.config.MCP.APIKey != "" || s.config.Auth.APIKey != ""
	uiLogin := s.config.Auth.UIPassword != ""
	allowOpen := s.config.Auth.AllowNoAuth && !uiLogin

	var refusal string
	if !anyKey && uiLogin {
		refusal = "Unauthorized: this InfraLens server requires a login for its UI (UI_PASSWORD), so /mcp is never open. " +
			"No MCP_API_KEY or API_KEY is configured on the server: set MCP_API_KEY there, then send it as " +
			"'Authorization: Bearer <key>' or 'X-API-Key: <key>'"
	} else if !anyKey && !allowOpen {
		refusal = "Unauthorized: no MCP_API_KEY or API_KEY is configured on this InfraLens server, so /mcp refuses every request. " +
			"Set MCP_API_KEY there, then send it as 'Authorization: Bearer <key>' or 'X-API-Key: <key>'"
	}

	h := mcp.NewHandler(
		mcp.Deps{Topology: s.topology, Inspections: s.store.Services(), DriftFeed: s.config.Drift.DetectionEnabled},
		mcp.Options{
			Version:             handlers.Version,
			Auth:                mcp.KeyAuthenticator(keys, allowOpen, s.config.Auth.HeaderName),
			UnauthorizedMessage: refusal,
			AllowedOrigins:      s.config.MCP.AllowedOrigins,
		},
	)

	switch {
	case s.config.MCP.APIKey != "" && s.config.Auth.APIKey != "":
		log.Info("MCP endpoint enabled at /mcp: requires MCP_API_KEY (read-only, recommended for AI clients) or API_KEY")
	case s.config.MCP.APIKey != "":
		log.Info("MCP endpoint enabled at /mcp: requires MCP_API_KEY")
	case s.config.Auth.APIKey != "":
		log.Info("MCP endpoint enabled at /mcp: requires API_KEY - set MCP_API_KEY to give AI clients a read-only key instead of the agents' key")
	case uiLogin:
		log.Warn("MCP endpoint at /mcp is refusing every request: UI_PASSWORD is set, so /mcp needs a key, and neither " +
			"MCP_API_KEY nor API_KEY is configured (ALLOW_NO_AUTH does not open it). Set MCP_API_KEY to enable it, " +
			"or MCP_ENABLED=false to silence this")
	case allowOpen:
		log.Warn("MCP endpoint enabled at /mcp WITHOUT authentication (no API_KEY or MCP_API_KEY, ALLOW_NO_AUTH=true, no UI_PASSWORD) - " +
			"anyone who can reach this server can query the topology through it; set MCP_API_KEY, or MCP_ENABLED=false to turn it off")
	default:
		log.Warn("MCP endpoint enabled at /mcp but no key is configured and ALLOW_NO_AUTH is not set - every request will be refused; set MCP_API_KEY to enable it")
	}
	return h
}

// withMCP dispatches exact /mcp requests to the MCP endpoint and everything
// else to rest (the global middleware chain).
func (s *Server) withMCP(rest http.Handler) http.Handler {
	if s.mcp == nil {
		return rest
	}
	// A router of its own so metrics get a route template, as elsewhere.
	r := mux.NewRouter()
	r.Handle("/mcp", s.mcp)
	r.Handle("/mcp/", s.mcp)
	chain := middleware.Chain(
		middleware.Recovery(),
		middleware.Metrics(),
		middleware.Logging(),
		middleware.BodyLimit(s.config.Server.MaxRequestBytes),
	)(r)

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/mcp" || req.URL.Path == "/mcp/" {
			chain.ServeHTTP(w, req)
			return
		}
		rest.ServeHTTP(w, req)
	})
}
