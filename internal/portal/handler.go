package portal

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"unicode"
)

const maxStaticAssetBytes = 8 << 20

type Credentials struct {
	Tokens               map[string]string
	Username             string
	Password             string
	AllowInsecureCookies bool
}

type Handler struct {
	registry      *Registry
	static        fs.FS
	tokens        map[string][32]byte
	authorized    map[string]struct{}
	username      [32]byte
	password      [32]byte
	sessions      *viewerSessions
	secureCookies bool
}

func NewHandler(registry *Registry, credentials Credentials, static fs.FS) (*Handler, error) {
	if registry == nil {
		return nil, errors.New("portal registry is required")
	}
	if !validUsername(credentials.Username) || len(credentials.Username) > 256 || len(credentials.Password) < 16 || len(credentials.Password) > 4096 {
		return nil, errors.New("portal viewer username must be valid (1-256 bytes) and password must contain 16-4096 bytes")
	}
	tokens := make(map[string][32]byte, len(credentials.Tokens))
	authorized := make(map[string]struct{}, len(credentials.Tokens))
	seen := make(map[[32]byte]struct{}, len(credentials.Tokens))
	for id, token := range credentials.Tokens {
		if !clusterIDPattern.MatchString(id) || len(token) < 32 {
			return nil, errors.New("portal registration credentials contain an invalid ID or token")
		}
		hash := sha256.Sum256([]byte(token))
		if _, exists := seen[hash]; exists {
			return nil, errors.New("portal registration tokens must be unique")
		}
		seen[hash] = struct{}{}
		tokens[id] = hash
		authorized[id] = struct{}{}
	}
	return &Handler{
		registry: registry, static: static, tokens: tokens, authorized: authorized,
		username: sha256.Sum256([]byte(credentials.Username)),
		password: sha256.Sum256([]byte(credentials.Password)),
		sessions: newViewerSessions(), secureCookies: !credentials.AllowInsecureCookies,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if r.URL.Path == "/healthz" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch {
	case r.URL.Path == "/api/auth/login":
		h.login(w, r)
	case r.URL.Path == "/api/auth/logout":
		h.logout(w, r)
	case r.URL.Path == "/api/auth/session":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.requireViewer(w, r) {
			w.WriteHeader(http.StatusNoContent)
		}
	case r.URL.Path == "/api/clusters":
		if !h.requireViewer(w, r) {
			return
		}
		if !sameSiteRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.registry.List(h.authorized))
	case strings.HasPrefix(r.URL.Path, "/api/registrations/"):
		if !sameSiteRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.register(w, r)
	case r.URL.Path == "/" || r.URL.Path == "/login":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		loggedIn := h.hasViewerSession(r)
		if r.URL.Path == "/" && !loggedIn {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.URL.Path == "/login" && loggedIn {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		h.serveFile(w, r, "portal.html")
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if !strings.HasPrefix(name, "assets/") || name == "assets" {
			http.NotFound(w, r)
			return
		}
		h.serveFile(w, r, name)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/registrations/")
	tokenHash, allowed := h.tokens[id]
	if !allowed || !authorizedBearer(r, tokenHash) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRegistrationBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var registration Registration
	if err := decoder.Decode(&registration); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid registration", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid registration", http.StatusBadRequest)
		return
	}
	if registration.ID != id || ValidateRegistration(registration) != nil {
		http.Error(w, "invalid registration", http.StatusBadRequest)
		return
	}
	if err := h.registry.Register(registration); err != nil {
		http.Error(w, "registration could not be saved", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	if h.static == nil {
		http.Error(w, "portal web assets are unavailable; build the portal frontend and embed it", http.StatusServiceUnavailable)
		return
	}
	file, err := h.static.Open(name)
	if err != nil {
		http.Error(w, "portal web assets are unavailable; build the portal frontend and embed it", http.StatusServiceUnavailable)
		return
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		_ = file.Close()
		http.Error(w, "portal web assets are unavailable; build the portal frontend and embed it", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxStaticAssetBytes+1))
	if err != nil || len(content) > maxStaticAssetBytes {
		http.Error(w, "portal web asset is unavailable or too large", http.StatusServiceUnavailable)
		return
	}
	if name == "portal.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", mimeType(name))
	}
	http.ServeContent(w, r, path.Base(name), info.ModTime(), bytes.NewReader(content))
}

func mimeType(name string) string {
	switch path.Ext(name) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".woff", ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

func (h *Handler) requireViewer(w http.ResponseWriter, r *http.Request) bool {
	if !h.hasViewerSession(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func authorizedBearer(r *http.Request, expected [32]byte) bool {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return false
	}
	actual := sha256.Sum256([]byte(strings.TrimPrefix(value, "Bearer ")))
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}

func validUsername(value string) bool {
	if value == "" || strings.Contains(value, ":") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func sameSiteRequest(r *http.Request) bool {
	value := strings.ToLower(r.Header.Get("Sec-Fetch-Site"))
	return value != "cross-site" && value != "same-site"
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}
