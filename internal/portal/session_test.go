package portal

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func loginForTest(t *testing.T, h *Handler) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": testUsername, "password": testPassword})
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || len(w.Result().Cookies()) != 1 {
		t.Fatalf("login status = %d, cookies = %d", w.Code, len(w.Result().Cookies()))
	}
	return w.Result().Cookies()[0]
}

func TestViewerSessionLifecycle(t *testing.T) {
	h, registry := testHandler(t, filepath.Join(t.TempDir(), "registry.json"))
	h.static = fstest.MapFS{"portal.html": &fstest.MapFile{Data: []byte("login shell")}}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.sessions.now = func() time.Time { return now }
	request := func(method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("must not trigger browser Basic Auth prompt")
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("auth responses must not be cached")
		}
		return w
	}
	if w := request("GET", "/", nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated root: %d %v", w.Code, w.Header())
	}
	if w := request("GET", "/login", nil); w.Code != 200 || w.Body.String() != "login shell" {
		t.Fatalf("public login shell: %d", w.Code)
	}
	cookie := loginForTest(t, h)
	if cookie.Name != "__Host-radar_portal_session" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != int(sessionLifetime.Seconds()) {
		t.Fatalf("unsafe session cookie attributes")
	}
	if w := request("GET", "/api/clusters", cookie); w.Code != 200 {
		t.Fatalf("authenticated list: %d", w.Code)
	}
	if w := request("GET", "/api/auth/session", cookie); w.Code != 204 {
		t.Fatalf("authenticated session: %d", w.Code)
	}
	if w := request("GET", "/", cookie); w.Code != 200 {
		t.Fatalf("authenticated overview: %d", w.Code)
	}
	if w := request("GET", "/login", cookie); w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatalf("authenticated login: %d", w.Code)
	}
	if w := request("PUT", "/api/registrations/cluster-a", cookie); w.Code != 401 {
		t.Fatalf("viewer cookie allowed registration: %d", w.Code)
	}
	if w := request("GET", "/api/auth/logout", cookie); w.Code != 405 {
		t.Fatalf("GET logout: %d", w.Code)
	}
	if w := request("POST", "/api/auth/logout", cookie); w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := request("GET", "/api/clusters", cookie); w.Code != 401 {
		t.Fatalf("revoked cookie accepted: %d", w.Code)
	}

	cookie = loginForTest(t, h)
	now = now.Add(sessionLifetime)
	if w := request("GET", "/api/clusters", cookie); w.Code != 401 {
		t.Fatalf("expired cookie accepted: %d", w.Code)
	}
	if len(h.sessions.expires) != 0 {
		t.Fatal("expired session not removed")
	}
	cookie = loginForTest(t, h)
	restarted, err := NewHandler(registry, Credentials{Username: testUsername, Password: testPassword}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/clusters", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	restarted.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("session survived restart")
	}
}

func TestLoginRejectsInvalidCredentialsAndCrossOriginMutations(t *testing.T) {
	valid, _ := json.Marshal(map[string]string{"username": testUsername, "password": testPassword})
	for _, tt := range []struct {
		name, method, path, body, contentType, origin, fetchSite string
		status                                                   int
	}{
		{"wrong password", "POST", "/api/auth/login", `{"username":"portal-viewer-user","password":"wrong"}`, "application/json", "", "", 401},
		{"wrong user", "POST", "/api/auth/login", `{"username":"wrong","password":"portal-viewer-password"}`, "application/json", "", "", 401},
		{"empty", "POST", "/api/auth/login", `{}`, "application/json", "", "", 401},
		{"malformed", "POST", "/api/auth/login", `{`, "application/json", "", "", 400},
		{"oversized", "POST", "/api/auth/login", strings.Repeat(" ", 17<<10), "application/json", "", "", 400},
		{"trailing JSON", "POST", "/api/auth/login", string(valid) + `{}`, "application/json", "", "", 400},
		{"unknown field", "POST", "/api/auth/login", `{"extra":true}`, "application/json", "", "", 400},
		{"GET login", "GET", "/api/auth/login", "", "", "", "", 405},
		{"form login", "POST", "/api/auth/login", string(valid), "text/plain", "", "", 415},
		{"cross-origin login", "POST", "/api/auth/login", string(valid), "application/json", "https://evil.test", "same-site", 403},
		{"cross-site login", "POST", "/api/auth/login", string(valid), "application/json", "", "cross-site", 403},
		{"null origin", "POST", "/api/auth/login", string(valid), "application/json", "null", "", 403},
		{"cross-origin logout", "POST", "/api/auth/logout", `{}`, "application/json", "https://evil.test", "", 403},
		{"form logout", "POST", "/api/auth/logout", `{}`, "text/plain", "", "", 415},
		{"same-origin", "POST", "/api/auth/login", string(valid), "application/json", "https://example.com", "same-origin", 204},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := testHandler(t, filepath.Join(t.TempDir(), "registry.json"))
			r := httptest.NewRequest(tt.method, "https://example.com"+tt.path, strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)
			r.Header.Set("Origin", tt.origin)
			r.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if tt.status != 204 && len(w.Result().Cookies()) != 0 {
				t.Fatal("rejected request sets cookie")
			}
			if strings.Contains(w.Body.String(), testPassword) || w.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("credential leak or Basic challenge")
			}
		})
	}
}

func TestViewerSessionRejectsBasicAndForgedCookies(t *testing.T) {
	h, _ := testHandler(t, filepath.Join(t.TempDir(), "registry.json"))
	r := httptest.NewRequest("GET", "/api/clusters", nil)
	r.SetBasicAuth(testUsername, testPassword)
	r.AddCookie(&http.Cookie{Name: h.cookieName(), Value: strings.Repeat("x", 43)})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("forged cookie or legacy Basic login accepted")
	}
	h.secureCookies = false
	cookie := loginForTest(t, h)
	if cookie.Secure || cookie.Name != "radar_portal_session" {
		t.Fatal("explicit local development cookie not configured")
	}
}

func TestLoginLimitsAndRotation(t *testing.T) {
	h, _ := testHandler(t, filepath.Join(t.TempDir(), "registry.json"))
	now := time.Now()
	h.sessions.now = func() time.Time { return now }
	cookie := loginForTest(t, h)
	body, _ := json.Marshal(map[string]string{"username": testUsername, "password": testPassword})
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Result().Cookies()[0].Value == cookie.Value {
		t.Fatal("session was not rotated")
	}
	if _, ok := h.sessions.expires[sha256.Sum256([]byte(cookie.Value))]; ok {
		t.Fatal("old session remains valid")
	}
	for i := 0; i < 18; i++ {
		loginForTest(t, h)
	}
	r = httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("login attempts are not bounded")
	}
	now = now.Add(time.Minute)
	loginForTest(t, h)
	for i := len(h.sessions.expires); i < maxViewerSessions; i++ {
		h.sessions.expires[sha256.Sum256([]byte(time.Unix(int64(i), 0).String()))] = now.Add(sessionLifetime)
	}
	r = httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || len(h.sessions.expires) != maxViewerSessions {
		t.Fatal("session storage is not bounded")
	}
}
