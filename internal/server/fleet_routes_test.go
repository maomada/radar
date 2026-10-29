package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFleetRouteOptInAndBasePath(t *testing.T) {
	for _, basePath := range []string{"", "/radar"} {
		for _, enabled := range []bool{false, true} {
			cfg := Config{ListenAddress: "127.0.0.1", BasePath: basePath}
			if enabled {
				cfg.FleetMCPHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
				cfg.FleetAPIHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/capabilities" {
						http.NotFound(w, r)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				})
			}
			srv := New(cfg)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost"+basePath+"/mcp-fleet", nil))
			want := http.StatusNotFound
			if enabled {
				want = http.StatusNoContent
			}
			if w.Code != want {
				t.Errorf("base=%q enabled=%v status=%d want=%d", basePath, enabled, w.Code, want)
			}
			w = httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost"+basePath+"/api/fleet/capabilities", nil))
			if w.Code != want {
				t.Errorf("API base=%q enabled=%v status=%d want=%d", basePath, enabled, w.Code, want)
			}
			srv.Stop()
		}
	}
}

func TestFleetAPIRouteRequiresSameOrigin(t *testing.T) {
	cfg := Config{
		ListenAddress: "127.0.0.1",
		FleetAPIHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	srv := New(cfg)
	defer srv.Stop()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/fleet/query/list_clusters", nil)
	request.Header.Set("Origin", "https://attacker.invalid")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin Fleet API status=%d want=%d", w.Code, http.StatusForbidden)
	}
}
