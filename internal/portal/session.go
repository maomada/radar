package portal

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const sessionLifetime = 8 * time.Hour
const maxViewerSessions = 1024

type viewerSessions struct {
	mu            sync.Mutex
	expires       map[[32]byte]time.Time
	now           func() time.Time
	loginAttempts []time.Time
}

func newViewerSessions() *viewerSessions {
	return &viewerSessions{expires: make(map[[32]byte]time.Time), now: time.Now}
}

func (s *viewerSessions) allowLogin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for len(s.loginAttempts) > 0 && !s.loginAttempts[0].After(now.Add(-time.Minute)) {
		s.loginAttempts = s.loginAttempts[1:]
	}
	if len(s.loginAttempts) >= 20 {
		return false
	}
	s.loginAttempts = append(s.loginAttempts, now)
	return true
}

func (h *Handler) cookieName() string {
	if h.secureCookies {
		return "__Host-radar_portal_session"
	}
	return "radar_portal_session"
}

func (h *Handler) sessionHash(r *http.Request) ([32]byte, bool) {
	cookie, err := r.Cookie(h.cookieName())
	if err != nil || len(cookie.Value) != 43 {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(cookie.Value)), true
}

func (h *Handler) hasViewerSession(r *http.Request) bool {
	key, ok := h.sessionHash(r)
	if !ok {
		return false
	}
	h.sessions.mu.Lock()
	defer h.sessions.mu.Unlock()
	expires, exists := h.sessions.expires[key]
	if !exists || !h.sessions.now().Before(expires) {
		delete(h.sessions.expires, key)
		return false
	}
	return true
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: h.cookieName(), Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: h.secureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func authMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !sameSiteRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return false
		}
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !authMutation(w, r) {
		return
	}
	if !h.sessions.allowLogin() {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decoder.Decode(&credentials) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	username := sha256.Sum256([]byte(credentials.Username))
	password := sha256.Sum256([]byte(credentials.Password))
	if subtle.ConstantTimeCompare(username[:], h.username[:])&subtle.ConstantTimeCompare(password[:], h.password[:]) != 1 {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	var random [32]byte
	_, _ = rand.Read(random[:])
	token := base64.RawURLEncoding.EncodeToString(random[:])
	h.sessions.mu.Lock()
	defer h.sessions.mu.Unlock()
	now := h.sessions.now()
	for key, expires := range h.sessions.expires {
		if !now.Before(expires) {
			delete(h.sessions.expires, key)
		}
	}
	if key, ok := h.sessionHash(r); ok {
		delete(h.sessions.expires, key)
	}
	if len(h.sessions.expires) >= maxViewerSessions {
		http.Error(w, "session capacity reached", http.StatusServiceUnavailable)
		return
	}
	h.sessions.expires[sha256.Sum256([]byte(token))] = now.Add(sessionLifetime)
	h.setSessionCookie(w, token, int(sessionLifetime.Seconds()))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !authMutation(w, r) {
		return
	}
	if key, ok := h.sessionHash(r); ok {
		h.sessions.mu.Lock()
		delete(h.sessions.expires, key)
		h.sessions.mu.Unlock()
	}
	h.setSessionCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
