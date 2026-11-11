package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLoginThrottle_ForgedForwardedForCannotLockOutAdmin reproduces the
// review's drain: with the default TRUSTED_PROXIES a directly connecting
// private-address peer is believed to be a proxy, so rotating
// X-Forwarded-For minted a fresh client bucket per request; 400 attempts
// drained the global cap and the admin got 429. Through the real handler
// chain: the attacker now exhausts only its own peer budget, and the admin
// still signs in - through the real proxy and directly.
func TestLoginThrottle_ForgedForwardedForCannotLockOutAdmin(t *testing.T) {
	env := newAuthEnv(t, testPassword)
	h := env.server.Handler()

	login := func(remote, xff, password string) int {
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(body)))
		req.RemoteAddr = remote
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	throttled := 0
	for i := 0; i < 400; i++ {
		xff := fmt.Sprintf("198.18.%d.%d", i/250, i%250+1)
		if code := login("10.0.0.66:40000", xff, "wrong guess"); code == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled < 400-60 {
		t.Errorf("forging peer was throttled only %d of 400 times; its peer budget should cap it near 60", throttled)
	}

	// The admin, behind the real proxy (the bundled nginx at 10.0.0.2), never
	// signed in from this address before.
	if code := login("10.0.0.2:51000", "203.0.113.5", testPassword); code != http.StatusOK {
		t.Errorf("admin via the real proxy: %d, want 200", code)
	}
	// And connecting directly from the LAN.
	if code := login("192.168.1.50:52000", "", testPassword); code != http.StatusOK {
		t.Errorf("admin connecting directly: %d, want 200", code)
	}
}
