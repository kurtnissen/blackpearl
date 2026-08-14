// Package httpserver exposes BlackPearl's process diagnostics.
package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

	"github.com/blackpearl-media/blackpearl/internal/domain"
)

// Readiness is the business-level dependency checked by /readyz.
type Readiness interface {
	Ready(ctx context.Context) error
}

// Options adds optional browser setup routes to the diagnostics server.
type Options struct {
	SetupAPI http.Handler
	UI       http.Handler
}

// New builds the diagnostics handler.
func New(readiness Readiness, logger *slog.Logger, configured ...Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", getOnly(func(writer http.ResponseWriter, _ *http.Request) {
		writeStatus(writer, http.StatusOK, `{"status":"ok"}`, logger)
	}))
	mux.HandleFunc("/readyz", getOnly(func(writer http.ResponseWriter, request *http.Request) {
		if err := readiness.Ready(request.Context()); err != nil {
			logger.WarnContext(request.Context(), "readiness check failed", "error", err)
			if errors.Is(err, domain.ErrNotConfigured) {
				writeStatus(writer, http.StatusServiceUnavailable, `{"status":"setup_required"}`, logger)
				return
			}
			writeStatus(writer, http.StatusServiceUnavailable, `{"status":"not_ready"}`, logger)
			return
		}
		writeStatus(writer, http.StatusOK, `{"status":"ready"}`, logger)
	}))
	if len(configured) > 0 {
		options := configured[0]
		if options.SetupAPI != nil {
			mux.Handle("/api/setup/", options.SetupAPI)
		}
		if options.UI != nil {
			mux.Handle("/", options.UI)
		}
	}
	return withRequestID(mux, logger)
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		next(writer, request)
	}
}

func withRequestID(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID, err := newRequestID()
		if err != nil {
			logger.ErrorContext(request.Context(), "generate request ID", "error", err)
			http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		writer.Header().Set("X-Request-Id", requestID)
		logger.DebugContext(request.Context(), "HTTP request", "requestId", requestID, "method", request.Method, "path", request.URL.Path)
		next.ServeHTTP(writer, request)
	})
}

func newRequestID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func writeStatus(writer http.ResponseWriter, status int, body string, logger *slog.Logger) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := writer.Write([]byte(body + "\n")); err != nil {
		logger.Warn("write diagnostics response", "error", err)
	}
}
