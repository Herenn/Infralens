package handlers

import (
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/Herenn/Infralens/backend/api/middleware"
)

// maxLoginBody caps the login request body; credentials are tiny.
const maxLoginBody = 4 << 10

// AuthHandler serves UI login, logout and session status.
type AuthHandler struct {
	// sessions is nil when UI login is disabled (UI_PASSWORD unset).
	sessions *middleware.SessionManager
}

// NewAuthHandler creates an AuthHandler. sessions may be nil.
func NewAuthHandler(sessions *middleware.SessionManager) *AuthHandler {
	return &AuthHandler{sessions: sessions}
}

// AuthStatusResponse tells the UI whether it must show a login screen.
type AuthStatusResponse struct {
	// LoginRequired is true when UI_PASSWORD is set.
	LoginRequired bool `json:"login_required"`
	// Authenticated is true when the caller holds a valid session or API key.
	Authenticated bool `json:"authenticated"`
	// Method is "session" or "api_key" when authenticated.
	Method string `json:"method,omitempty"`
	// Username and ExpiresAt describe the caller's session.
	Username  string `json:"username,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// HandleStatus reports whether login is required and whether the caller is
// logged in. Always public.
func (h *AuthHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	resp := AuthStatusResponse{}
	if h.sessions != nil {
		resp.LoginRequired = true
		id := h.sessions.Authenticate(r)
		switch id.Method {
		case middleware.MethodSession:
			// A foreign page reading this with the victim's cookie would
			// learn the username and session expiry; answer it as anonymous.
			if !h.sessions.SameOrigin(r) {
				break
			}
			resp.Authenticated = true
			resp.Method = id.Method
			resp.Username = h.sessions.Username()
			resp.ExpiresAt = id.Session.Expires.UTC().Format(time.RFC3339)
		case middleware.MethodAPIKey:
			resp.Authenticated = true
			resp.Method = id.Method
		}
	}
	writeAuthJSON(w, http.StatusOK, resp)
}

// HandleLogin checks the admin credentials and starts a session. Attempts
// are throttled per client and globally; a throttled attempt is refused
// before the password is looked at, so it reveals nothing about it.
func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if h.sessions == nil {
		writeAuthJSON(w, http.StatusNotFound, map[string]string{"error": "UI login is not enabled (UI_PASSWORD is not set)"})
		return
	}
	// Login CSRF is low-stakes with a single account, but the check is
	// cheap and keeps every cookie-setting request same-origin.
	if !h.sessions.SameOrigin(r) {
		writeAuthJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	// Requiring JSON also means a plain HTML form on another site cannot
	// submit here at all.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeAuthJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return
	}

	ok, retryAfter := h.sessions.AllowLoginAttempt(r)
	if !ok {
		secs := int(retryAfter.Round(time.Second) / time.Second)
		if secs < 1 {
			secs = 1
		}
		log.WithField("remote", r.RemoteAddr).Warn("UI login throttled")
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeAuthJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"error":       "too many login attempts, try again later",
			"retry_after": secs,
		})
		return
	}

	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&req); err != nil {
		writeAuthJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if !h.sessions.CheckCredentials(req.Username, req.Password) {
		log.WithField("remote", r.RemoteAddr).Warn("UI login failed: invalid username or password")
		writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
		return
	}
	h.sessions.LoginSucceeded(r)

	// A fresh session ID on every login (no fixation); retire any session
	// the browser was still holding.
	if old, ok := h.sessions.SessionFromRequest(r); ok {
		if err := h.sessions.Revoke(r.Context(), old); err != nil {
			log.WithError(err).Error("UI login: could not persist revocation of the previous session")
		}
	}
	s := h.sessions.NewSession(w, r)
	log.WithField("remote", r.RemoteAddr).Info("UI login succeeded")

	writeAuthJSON(w, http.StatusOK, AuthStatusResponse{
		LoginRequired: true,
		Authenticated: true,
		Method:        middleware.MethodSession,
		Username:      h.sessions.Username(),
		ExpiresAt:     s.Expires.UTC().Format(time.RFC3339),
	})
}

// HandleLogout revokes the caller's session and clears its cookie. It
// succeeds whether or not there was a session to end.
func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if h.sessions == nil {
		writeAuthJSON(w, http.StatusNotFound, map[string]string{"error": "UI login is not enabled (UI_PASSWORD is not set)"})
		return
	}
	if s, ok := h.sessions.SessionFromRequest(r); ok {
		if !h.sessions.SameOrigin(r) {
			writeAuthJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		if err := h.sessions.Revoke(r.Context(), s); err != nil {
			// The session is already dead on this process and the cookie is
			// cleared below; only restarts and other replicas would not know.
			log.WithError(err).Error("UI logout: could not persist the revocation - " +
				"it may not survive a restart or reach other replicas")
		}
		log.WithField("remote", r.RemoteAddr).Info("UI logout")
	}
	h.sessions.ClearCookie(w, r)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func writeAuthJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
