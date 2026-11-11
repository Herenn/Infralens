package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Authenticator decides whether a request may use the endpoint. allowed=false
// is answered with 401. authenticated reports whether the caller proved
// possession of a key, as opposed to being let in because the endpoint is
// deliberately open; tools use it to withhold data the public read API does
// not serve (see get_service's process fingerprint).
//
// An Authenticator must only look at explicit credential headers. Cookies
// must never authenticate /mcp: it is a programmatic endpoint, and accepting
// ambient browser credentials would leave the Origin check as the only
// defence against cross-site requests.
type Authenticator func(r *http.Request) (allowed, authenticated bool)

// KeyAuthenticator is the /mcp access policy:
//
//   - any key configured: a request must present one of acceptedKeys, as
//     "Authorization: Bearer <key>", "X-API-Key: <key>", or any of
//     extraHeaders (e.g. a custom API_KEY_HEADER);
//   - no key configured: open (unauthenticated) only when allowOpen is set,
//     otherwise every request is refused.
//
// Empty keys are ignored. Comparison is constant-time over SHA-256 digests,
// so neither the key's contents nor its length leak through timing, and every
// accepted key is checked regardless of which one matches.
func KeyAuthenticator(acceptedKeys []string, allowOpen bool, extraHeaders ...string) Authenticator {
	var digests [][32]byte
	for _, k := range acceptedKeys {
		if k != "" {
			digests = append(digests, sha256.Sum256([]byte(k)))
		}
	}
	headers := []string{"X-API-Key"}
	for _, h := range extraHeaders {
		if h != "" && !strings.EqualFold(h, "X-API-Key") && !strings.EqualFold(h, "Authorization") {
			headers = append(headers, h)
		}
	}

	return func(r *http.Request) (bool, bool) {
		if len(digests) == 0 {
			return allowOpen, false
		}
		var presented []string
		if a := r.Header.Get("Authorization"); len(a) > len("Bearer ") && strings.EqualFold(a[:len("Bearer ")], "Bearer ") {
			presented = append(presented, strings.TrimSpace(a[len("Bearer "):]))
		}
		for _, h := range headers {
			if v := r.Header.Get(h); v != "" {
				presented = append(presented, v)
			}
		}
		match := 0
		for _, p := range presented {
			pd := sha256.Sum256([]byte(p))
			for i := range digests {
				match |= subtle.ConstantTimeCompare(pd[:], digests[i][:])
			}
		}
		return match == 1, match == 1
	}
}

type authContextKey struct{}

func withAuthenticated(ctx context.Context, authenticated bool) context.Context {
	return context.WithValue(ctx, authContextKey{}, authenticated)
}

// isAuthenticated reports whether the current request presented a valid key.
func isAuthenticated(ctx context.Context) bool {
	v, _ := ctx.Value(authContextKey{}).(bool)
	return v
}
