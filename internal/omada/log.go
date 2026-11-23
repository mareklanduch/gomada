package omada

import (
	"log/slog"
	"net/http"
	"time"
)

// LogRequests makes hc log one line per request: method, URL without its
// query, status, and duration. Headers and bodies are never logged, so
// secrets and tokens cannot leak through the log.
func LogRequests(hc *http.Client, logger *slog.Logger) {
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	hc.Transport = roundTripper(func(req *http.Request) (*http.Response, error) {
		start := time.Now()
		resp, err := next.RoundTrip(req)
		u := *req.URL
		u.RawQuery = ""
		attrs := []any{"method", req.Method, "url", u.String(), "duration", time.Since(start).Round(time.Millisecond)}
		if err != nil {
			logger.Debug("request failed", append(attrs, "error", err)...)
			return nil, err
		}
		logger.Debug("request", append(attrs, "status", resp.StatusCode)...)
		return resp, nil
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
