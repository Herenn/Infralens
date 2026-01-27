package middleware

import (
	"bufio"
	"net"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
// It also implements http.Hijacker to support WebSocket connections.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.size += n
	return n, err
}

// Hijack implements http.Hijacker for WebSocket support.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Flush implements http.Flusher for streaming support.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Logging creates a request logging middleware.
func Logging() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap the response writer
			wrapped := &responseWriter{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			// Call the next handler
			next.ServeHTTP(wrapped, r)

			// Calculate duration
			duration := time.Since(start)

			// Determine log level based on status code
			logger := log.WithFields(log.Fields{
				"method":     r.Method,
				"path":       r.URL.Path,
				"status":     wrapped.statusCode,
				"duration":   duration.String(),
				"size":       wrapped.size,
				"remote":     r.RemoteAddr,
				"user_agent": r.UserAgent(),
			})

			// Skip logging for health checks to reduce noise
			if r.URL.Path == "/health" || r.URL.Path == "/ready" {
				return
			}

			switch {
			case wrapped.statusCode >= 500:
				logger.Error("Request failed with server error")
			case wrapped.statusCode >= 400:
				logger.Warn("Request failed with client error")
			case wrapped.statusCode >= 300:
				logger.Debug("Request redirected")
			default:
				logger.Debug("Request completed")
			}
		})
	}
}

// Recovery creates a panic recovery middleware.
func Recovery() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.WithFields(log.Fields{
						"error":  err,
						"method": r.Method,
						"path":   r.URL.Path,
						"remote": r.RemoteAddr,
					}).Error("Panic recovered")

					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// Chain applies multiple middleware in order.
func Chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(final http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}
