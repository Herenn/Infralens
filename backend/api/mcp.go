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
//   - Auth: it enforces its own policy (mcp.KeyAuthenticator), which accepts
//     the read-only MCP_API_KEY as well as API_KEY. MCP_API_KEY is valid
//     here and nowhere else, because only this branch knows about it. Any
//     session/cookie authentication added to the global chain never sees
//     /mcp, so cookies cannot authenticate it by construction.
//   - CORS: the global rs/cors layer would answer preflights itself, with a
//     header list that lacks the MCP headers and an origin list
//     (CORS_ORIGINS) that disagrees with MCP_ALLOWED_ORIGINS. The MCP handler
//     owns one coherent cross-origin policy instead.
//
// Recovery, metrics, logging and the body limit still apply.

// newMCPEndpoint builds the /mcp handler from config.
func (s *Server) newMCPEndpoint() http.Handler {
	keys := []string{s.config.MCP.APIKey, s.config.Auth.APIKey}
	allowOpen := s.config.Auth.AllowNoAuth

	h := mcp.NewHandler(
		mcp.Deps{Topology: s.topology, Inspections: s.store.Services()},
		mcp.Options{
			Version:        handlers.Version,
			Auth:           mcp.KeyAuthenticator(keys, allowOpen, s.config.Auth.HeaderName),
			AllowedOrigins: s.config.MCP.AllowedOrigins,
		},
	)

	switch {
	case s.config.MCP.APIKey != "" && s.config.Auth.APIKey != "":
		log.Info("MCP endpoint enabled at /mcp: requires MCP_API_KEY (read-only, recommended for AI clients) or API_KEY")
	case s.config.MCP.APIKey != "":
		log.Info("MCP endpoint enabled at /mcp: requires MCP_API_KEY")
	case s.config.Auth.APIKey != "":
		log.Info("MCP endpoint enabled at /mcp: requires API_KEY - set MCP_API_KEY to give AI clients a read-only key instead of the agents' key")
	case allowOpen:
		log.Warn("MCP endpoint enabled at /mcp WITHOUT authentication (no API_KEY or MCP_API_KEY, ALLOW_NO_AUTH=true) - " +
			"anyone who can reach this server can query the topology through it; set MCP_API_KEY, or MCP_ENABLED=false to turn it off")
	default:
		log.Warn("MCP endpoint enabled at /mcp but no key is configured and ALLOW_NO_AUTH is not set - every request will be refused")
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
