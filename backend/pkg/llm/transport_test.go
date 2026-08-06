package llm

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSanitizeTransportErrorStripsCredentialFromURL is a regression test for
// a real credential leak. Gemini sends its API key as a URL query parameter
// (the standard way Google's API expects it), and Go's http.Client wraps any
// transport-level failure - refused connection, DNS failure, timeout, no
// response required - in a *url.Error whose Error() string embeds the full
// request URL. Without this sanitization, a plain network hiccup talking to
// Gemini would put a live API key directly into this process's logs and into
// the HTTP response returned to whoever called the AI documentation
// endpoint - not just an authentication failure, any transport failure at
// all, which given real traffic is routine rather than exotic.
//
// This drives an actual failing request through a real *http.Client rather
// than constructing a *url.Error by hand, so it exercises exactly what
// production code sees.
func TestSanitizeTransportErrorStripsCredentialFromURL(t *testing.T) {
	const fakeKey = "AIzaSyD-this-would-be-a-live-secret-key-value"

	client := &http.Client{Timeout: 2 * time.Second}
	// Port 1 is a real port that nothing binds to; the connection is refused
	// immediately rather than timing out, keeping the test fast.
	url := "http://127.0.0.1:1/v1beta/models/gemini-pro:generateContent?key=" + fakeKey

	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	_, doErr := client.Do(req)
	if doErr == nil {
		t.Fatal("expected the request to fail (nothing listens on 127.0.0.1:1); it succeeded instead")
	}

	// Confirm the premise: an unsanitized error really does contain the key,
	// so the test is proving something and not vacuously passing.
	if !strings.Contains(doErr.Error(), fakeKey) {
		t.Fatalf("test premise invalid: raw client.Do error does not contain the key; got %q", doErr.Error())
	}

	sanitized := sanitizeTransportError(doErr)

	if strings.Contains(sanitized.Error(), fakeKey) {
		t.Errorf("sanitizeTransportError did not remove the credential: %q", sanitized.Error())
	}
	if strings.Contains(sanitized.Error(), url) {
		t.Errorf("sanitizeTransportError did not remove the URL: %q", sanitized.Error())
	}
}

// TestSanitizeTransportErrorPassesThroughOtherErrors confirms the function is
// a no-op for errors that were never a *url.Error, so it can be applied
// unconditionally at every call site without masking unrelated failures.
func TestSanitizeTransportErrorPassesThroughOtherErrors(t *testing.T) {
	original := errWithMessage("some other kind of failure")

	got := sanitizeTransportError(original)

	if got != original {
		t.Errorf("expected a non-url.Error to pass through unchanged, got %v", got)
	}
}

type errWithMessage string

func (e errWithMessage) Error() string { return string(e) }
