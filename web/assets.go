// Package webui embeds BlackPearl's static browser setup application.
package webui

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:out
var assets embed.FS

// Handler serves the immutable static export with setup-safe response headers.
func Handler() (http.Handler, error) {
	content, err := fs.Sub(assets, "out")
	if err != nil {
		return nil, errors.New("open embedded setup UI")
	}
	files := http.FileServer(http.FS(content))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		// Next static export uses inline Flight bootstrap records to hydrate the
		// otherwise immutable, embedded UI. All executable assets and network
		// connections remain same-origin only.
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		cleaned := path.Clean("/" + request.URL.Path)
		if strings.Contains(request.URL.Path, "..") || cleaned != request.URL.Path && !(request.URL.Path == "" && cleaned == "/") {
			http.NotFound(writer, request)
			return
		}
		files.ServeHTTP(writer, request)
	}), nil
}
