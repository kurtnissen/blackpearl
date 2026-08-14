// Package setup exposes the localhost-only browser setup API.
package setup

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/blackpearl-media/blackpearl/internal/domain"
	setupservice "github.com/blackpearl-media/blackpearl/internal/service/setup"
)

const maximumRequestBytes = 128 * 1024
const (
	setupSessionHeader   = "X-BlackPearl-Session"
	setupBootstrapHeader = "X-BlackPearl-Bootstrap"
)

// Service is the business boundary consumed by setup HTTP handlers.
type Service interface {
	Status() setupservice.Status
	Discover(ctx context.Context, token string) ([]domain.MediaCandidate, error)
	Apply(ctx context.Context, request setupservice.ApplyRequest) (domain.SetupManifest, error)
	AuthorizeSetup(ctx context.Context, suppliedToken string, session string, bootstrap string) error
	IssueSession(ctx context.Context, token string) (string, error)
}

type handler struct {
	service Service
	csrf    string
	logger  *slog.Logger
}

// New constructs a setup API with a process-local CSRF secret.
func New(service Service, configuredLogger ...*slog.Logger) (http.Handler, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return nil, errors.New("generate setup CSRF token")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if len(configuredLogger) > 0 && configuredLogger[0] != nil {
		logger = configuredLogger[0]
	}
	return &handler{service: service, csrf: hex.EncodeToString(value), logger: logger}, nil
}

func (h *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	securityHeaders(writer.Header())
	switch request.URL.Path {
	case "/api/setup/status":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		status := h.service.Status()
		writeJSON(writer, http.StatusOK, struct {
			setupservice.Status
			CSRFToken string `json:"csrfToken"`
		}{Status: status, CSRFToken: h.csrf})
	case "/api/setup/discover":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		if !h.authorizeBrowser(request) {
			writeError(writer, http.StatusForbidden, "forbidden", "Setup requests are accepted only from this local page.")
			return
		}
		var input struct {
			Token string `json:"token,omitempty"`
		}
		if err := decodeJSON(writer, request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "Enter a valid setup request.")
			return
		}
		if err := h.service.AuthorizeSetup(request.Context(), input.Token, request.Header.Get(setupSessionHeader), request.Header.Get(setupBootstrapHeader)); err != nil {
			h.writeServiceError(writer, request, err)
			return
		}
		items, err := h.service.Discover(request.Context(), input.Token)
		if err != nil {
			h.writeServiceError(writer, request, err)
			return
		}
		sessionToken := input.Token
		if h.service.Status().TokenConfigured {
			sessionToken = ""
		}
		if err := h.issueSession(writer, request, sessionToken); err != nil {
			h.writeServiceError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, struct {
			Candidates []domain.MediaCandidate `json:"candidates"`
		}{Candidates: items})
	case "/api/setup/configuration":
		if request.Method != http.MethodPut {
			methodNotAllowed(writer, http.MethodPut)
			return
		}
		if !h.authorizeBrowser(request) {
			writeError(writer, http.StatusForbidden, "forbidden", "Setup requests are accepted only from this local page.")
			return
		}
		var input setupservice.ApplyRequest
		if err := decodeJSON(writer, request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request", "Enter a valid setup request.")
			return
		}
		if err := h.service.AuthorizeSetup(request.Context(), input.Token, request.Header.Get(setupSessionHeader), request.Header.Get(setupBootstrapHeader)); err != nil {
			h.writeServiceError(writer, request, err)
			return
		}
		manifest, err := h.service.Apply(request.Context(), input)
		if err != nil {
			h.writeServiceError(writer, request, err)
			return
		}
		if err := h.issueSession(writer, request, ""); err != nil {
			h.writeServiceError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, struct {
			Selected      domain.SetupConfiguration   `json:"selected"`
			SelectedItems []domain.SetupConfiguration `json:"selectedItems"`
		}{Selected: manifest.Items[0], SelectedItems: manifest.Items})
	default:
		http.NotFound(writer, request)
	}
}

func (h *handler) writeServiceError(writer http.ResponseWriter, request *http.Request, err error) {
	h.logger.WarnContext(request.Context(), "setup request failed", "method", request.Method, "path", request.URL.Path, "error", err)
	writeServiceError(writer, err)
}

func (h *handler) authorizeBrowser(request *http.Request) bool {
	if !loopbackHost(request.Host) {
		return false
	}
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || !loopbackHost(origin.Host) || !strings.EqualFold(origin.Host, request.Host) {
		return false
	}
	provided := request.Header.Get("X-BlackPearl-CSRF")
	return len(provided) == len(h.csrf) && subtle.ConstantTimeCompare([]byte(provided), []byte(h.csrf)) == 1
}

func (h *handler) issueSession(writer http.ResponseWriter, request *http.Request, token string) error {
	value, err := h.service.IssueSession(request.Context(), token)
	if err != nil {
		return err
	}
	writer.Header().Set(setupSessionHeader, value)
	return nil
}

func loopbackHost(hostport string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	} else if strings.Count(hostport, ":") > 1 {
		host = strings.Trim(hostport, "[]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeServiceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, setupservice.ErrSetupUnauthorized):
		writeError(writer, http.StatusUnauthorized, "setup_not_paired", "This browser is not paired with BlackPearl. Reopen the setup page from the BlackPearl launcher and try again.")
	case errors.Is(err, setupservice.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "unauthorized", "That TorBox API key is invalid or expired. Open TorBox Settings, select Copy API Key, and try again.")
	case errors.Is(err, setupservice.ErrInvalidSelection):
		writeError(writer, http.StatusUnprocessableEntity, "invalid_selection", "That video is no longer available for setup.")
	default:
		writeError(writer, http.StatusServiceUnavailable, "provider_unavailable", "TorBox is temporarily unavailable. Try again shortly.")
	}
}

func securityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}

func writeError(writer http.ResponseWriter, status int, code string, message string) {
	writeJSON(writer, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	content, err := json.Marshal(value)
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := writer.Write(append(content, '\n')); err != nil {
		return
	}
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "That operation is not supported.")
}
