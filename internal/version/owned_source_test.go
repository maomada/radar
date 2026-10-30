package version

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOwnedReleaseSourceWithoutProxy(t *testing.T) {
	previousURL, previousGitHub, previousVersion := releasesURL, githubURL, Current
	releasesURL, Current = "", "0.1.0"
	t.Cleanup(func() {
		releasesURL, githubURL, Current = previousURL, previousGitHub, previousVersion
	})

	for _, status := range []int{http.StatusOK, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.RawQuery != "" {
					t.Errorf("direct release lookup must not send installation metadata: %s", r.URL.RawQuery)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","html_url":"https://github.com/maomada/radar/releases/tag/v0.2.0","body":"release fixture","assets":[]}`))
			}))
			defer server.Close()
			githubURL = server.URL

			result := fetchLatestRelease(context.Background(), checkOptions{manual: true})
			if status == http.StatusOK {
				if !result.UpdateAvail || result.LatestVersion != "0.2.0" || result.Error != "" {
					t.Fatalf("unexpected owned release result: %+v", result)
				}
			} else if result.Error == "" || result.UpdateAvail {
				t.Fatalf("unreadable releases must remain unknown, without upstream fallback: %+v", result)
			}
			if requests != 1 {
				t.Fatalf("direct lookup made %d requests, want 1", requests)
			}
			if err := RelayUpdateCheck(context.Background()); err != nil || requests != 1 {
				t.Fatalf("an unconfigured relay must make no request: err=%v requests=%d", err, requests)
			}

			release, err := FetchRelease(context.Background())
			if status == http.StatusOK {
				if err != nil || release.TagName != "v0.2.0" {
					t.Fatalf("desktop lookup did not use the owned source: release=%+v err=%v", release, err)
				}
			} else if err == nil {
				t.Fatal("desktop lookup must not hide an unavailable owned release")
			}
			if requests != 2 {
				t.Fatalf("desktop lookup used a different release source: requests=%d", requests)
			}
		})
	}

	t.Run("transport error", func(t *testing.T) {
		githubURL = ":invalid"
		result := fetchLatestRelease(context.Background(), checkOptions{manual: true})
		if result.Error == "" || result.UpdateAvail {
			t.Fatalf("invalid release URL must fail closed: %+v", result)
		}
	})
}

func TestOwnedReleaseDefaults(t *testing.T) {
	if releasesURL != "" || githubURL != "https://api.github.com/repos/maomada/radar/releases/latest" {
		t.Fatalf("unexpected release sources: relay=%q github=%q", releasesURL, githubURL)
	}
	if !strings.HasPrefix(defaultReleasesURL, "https://github.com/maomada/radar/") {
		t.Fatalf("unexpected release page: %s", defaultReleasesURL)
	}
}
