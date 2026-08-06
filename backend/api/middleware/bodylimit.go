package middleware

import "net/http"

// BodyLimit caps how much of a request body a handler can read.
//
// Every ingest handler decodes JSON straight off r.Body. Without a cap a
// single request can drive the process out of memory, so this wraps the body
// in an http.MaxBytesReader: reads past the limit fail, and the decoder
// surfaces that as a normal error rather than allocating without bound.
//
// A non-positive limit disables the middleware.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if maxBytes <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
