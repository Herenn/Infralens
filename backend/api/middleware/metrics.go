package middleware

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/Herenn/Infralens/backend/pkg/metrics"
)

// metricsResponseWriter wraps http.ResponseWriter to capture metrics.
type metricsResponseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int
}

func (w *metricsResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *metricsResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.size += n
	return n, err
}

// Hijack implements http.Hijacker for WebSocket support.
func (w *metricsResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Flush implements http.Flusher for streaming support.
func (w *metricsResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Metrics creates a middleware that collects Prometheus metrics for HTTP requests.
func Metrics() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip metrics endpoint to avoid self-referential metrics
			if r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()

			// Track active requests
			metrics.HTTPActiveRequests.Inc()
			defer metrics.HTTPActiveRequests.Dec()

			// Record request size
			if r.ContentLength > 0 {
				metrics.HTTPRequestSize.WithLabelValues(r.Method, metrics.NormalizePath(r.URL.Path)).Observe(float64(r.ContentLength))
			}

			// Wrap response writer
			wrapped := &metricsResponseWriter{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			// Call next handler
			next.ServeHTTP(wrapped, r)

			// Record metrics
			duration := time.Since(start).Seconds()
			path := metrics.NormalizePath(r.URL.Path)

			metrics.HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
			metrics.HTTPRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(wrapped.statusCode)).Inc()
			metrics.HTTPResponseSize.WithLabelValues(r.Method, path).Observe(float64(wrapped.size))
		})
	}
}
