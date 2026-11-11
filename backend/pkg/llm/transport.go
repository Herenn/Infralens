package llm

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// sanitizeTransportError strips the request URL from an HTTP transport
// failure before it is logged or wrapped into an error a client might see.
//
// Go's http.Client wraps any error that occurs before a response is received
// (refused connection, DNS failure, TLS failure, timeout, context
// cancellation) in a *url.Error, whose Error() string embeds the full
// request URL - query string included. The Gemini provider sends its API
// key as a URL query parameter (?key=...), which is how Google's API expects
// it; that means a plain network hiccup talking to Gemini - not even an
// authentication failure - would otherwise place a live API key directly
// into this process's logs and into the response sent back to whoever called
// the AI endpoint. Verified live: a *url.Error from a refused connection
// renders as `Post "https://...?key=<the actual key>": dial tcp ...`.
//
// Applied uniformly at every provider's request call site rather than only
// Gemini's, since the failure mode is generic to how net/http reports
// transport errors and nothing prevents a future provider or edit from
// reintroducing a credential in a URL. The underlying cause (timeout,
// connection refused, DNS failure, ...) is preserved via error wrapping;
// only the URL itself is dropped.
func sanitizeTransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("request failed: %w", urlErr.Err)
	}
	return err
}

// newCloudClient returns the http.Client used for the cloud providers
// (OpenAI, Anthropic, Gemini). Those requests carry the API key in a custom
// header (x-api-key, x-goog-api-key) or Authorization. Go's client drops
// Authorization on a cross-host redirect but forwards custom headers, so a
// redirect - which none of these APIs legitimately issue - could hand the key
// to another host. Redirects are therefore not followed: the 3xx response is
// returned as-is and surfaces as an ordinary HTTP error.
func newCloudClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// maxListBody caps how much of a list-models response is read; real
// responses are tens of KB.
const maxListBody = 4 << 20

// maxCompletionBody caps how much of a completion response is read. The
// largest legitimate answer (128K output tokens of JSON-escaped text) is well
// under this; anything bigger fails JSON parsing instead of exhausting memory.
const maxCompletionBody = 8 << 20

// readBody reads at most limit bytes of a response body.
func readBody(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}
