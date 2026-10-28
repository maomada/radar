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
			srv.Stop()
		}
	}
}
