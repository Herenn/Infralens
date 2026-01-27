package middleware

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"

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

// getPathTemplate extracts the route template from gorilla/mux.
// This is more efficient than regex-based path normalization and
// guarantees low cardinality labels for Prometheus metrics.
func getPathTemplate(r *http.Request) string {
	route := mux.CurrentRoute(r)
	if route == nil {
		return "unmatched"
	}

	pathTemplate, err := route.GetPathTemplate()
	if err != nil || pathTemplate == "" {
		return "unmatched"
	}

	return pathTemplate
}

// Metrics creates a middleware that collects Prometheus metrics for HTTP requests.
// It uses gorilla/mux route templates for path labels, ensuring low cardinality.
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

			// Wrap response writer
			wrapped := &metricsResponseWriter{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			// Call next handler (route matching happens here)
			next.ServeHTTP(wrapped, r)

			// Get path template AFTER routing (so mux.CurrentRoute works)
			pathTemplate := getPathTemplate(r)

			// Record request size (use template for low cardinality)
			if r.ContentLength > 0 {
				metrics.HTTPRequestSize.WithLabelValues(r.Method, pathTemplate).Observe(float64(r.ContentLength))
			}

			// Record metrics using the route template (not the actual path)
			duration := time.Since(start).Seconds()

			metrics.HTTPRequestDuration.WithLabelValues(r.Method, pathTemplate).Observe(duration)
			metrics.HTTPRequestsTotal.WithLabelValues(r.Method, pathTemplate, strconv.Itoa(wrapped.statusCode)).Inc()
			metrics.HTTPResponseSize.WithLabelValues(r.Method, pathTemplate).Observe(float64(wrapped.size))
		})
	}
}
