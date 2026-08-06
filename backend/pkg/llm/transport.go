package llm

import (
	"errors"
	"fmt"
	"net/url"
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
