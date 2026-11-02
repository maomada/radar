package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudEntryPointsRejectBeforeNetwork(t *testing.T) {
	if os.Getenv("KUBEATLAS_TEST_CLOUD_HELPER") == "1" {
		os.Args = append([]string{"kubeatlas"}, strings.Fields(os.Getenv("KUBEATLAS_TEST_CLOUD_ARGS"))...)
		main()
		os.Exit(0)
	}

	var requests atomic.Int32
	sentinel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sentinel.Close()

	for _, tc := range []struct {
		args     string
		cloudURL string
		mode     bool
		want     string
	}{
		{
			args: "cloud install --hub-url=" + sentinel.URL,
			want: "upstream Cloud commands are not available in KubeAtlas",
		},
		{
			args:     "--cloud-url=" + sentinel.URL,
			cloudURL: sentinel.URL,
			want:     "upstream Cloud connections are not supported by KubeAtlas",
		},
		{
			args:     "--no-browser",
			cloudURL: sentinel.URL,
			mode:     true,
			want:     "upstream Cloud connections are not supported by KubeAtlas",
		},
	} {
		t.Run(tc.args, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			home := t.TempDir()
			kubeconfig := home + "/kubeconfig"
			config := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: %s\n  name: sentinel\ncontexts:\n- context:\n    cluster: sentinel\n    user: sentinel\n  name: sentinel\ncurrent-context: sentinel\nusers:\n- name: sentinel\n", sentinel.URL)
			if err := os.WriteFile(kubeconfig, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCloudEntryPointsRejectBeforeNetwork$")
			cmd.Env = cloudTestEnv(tc.args, tc.cloudURL, home, kubeconfig)
			if tc.mode {
				cmd.Env = append(cmd.Env, "KUBEATLAS_CLOUD_MODE=true")
			}
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("CLI unexpectedly succeeded: %s", output)
			}
			if !strings.Contains(string(output), tc.want) {
				t.Fatalf("CLI output %q does not contain expected rejection %q", output, tc.want)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("sentinel received %d requests before rejection", got)
	}
}

func cloudTestEnv(args, cloudURL, home, kubeconfig string) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "KUBECONFIG=") || strings.HasPrefix(entry, "KUBEATLAS_CLOUD_") || strings.HasPrefix(entry, "KUBEATLAS_TEST_CLOUD_") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "HOME="+home, "KUBECONFIG="+kubeconfig, "KUBEATLAS_TEST_CLOUD_HELPER=1", "KUBEATLAS_TEST_CLOUD_ARGS="+args)
	if cloudURL != "" {
		env = append(env, "KUBEATLAS_CLOUD_URL="+cloudURL)
	}
	return env
}
