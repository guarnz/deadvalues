package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guarnz/deadvalues/internal/retry"
)

func TestMain(m *testing.M) {
	retry.Delays = []time.Duration{0, 0}
	os.Exit(m.Run())
}

func TestLocalChartGetsWhatItCanOfItsDependencies(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/app/Chart.yaml": "apiVersion: v2\nname: app\nversion: 1.0.0\ndependencies:\n" +
			"  - {name: lib, version: 1.0.0, repository: \"file://../../shared/lib\"}\n" +
			"  - {name: vendored, version: 1.0.0, repository: \"https://example.invalid\"}\n" +
			"  - {name: down, version: 1.0.0, repository: \"" + srv.URL + "\"}\n" +
			"  - {name: nowhere, version: 1.0.0}\n" +
			"  - {name: gone, version: 1.0.0, repository: \"file://../gone\"}\n",
		"apps/app/charts/vendored/Chart.yaml": "apiVersion: v2\nname: vendored\nversion: 1.0.0\n",
		"shared/lib/Chart.yaml":               "apiVersion: v2\nname: lib\nversion: 1.0.0\n",
	})
	l := NewLoader(t.TempDir(), false)
	for range 2 {
		c, err := l.Load(context.Background(), Ref{Chart: filepath.Join(root, "apps", "app")})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, d := range c.Dependencies() {
			names = append(names, d.Name())
		}
		if got := strings.Join(names, ","); got != "vendored,lib" {
			t.Errorf("dependencies = %s", got)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("a repository that failed was asked %d times in one run", got)
	}
}

func TestIndexIsFetchedAgainAfterAGatewayError(t *testing.T) {
	retry.Reset()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "upstream timed out", http.StatusGatewayTimeout)
			return
		}
		fmt.Fprint(w, "apiVersion: v1\nentries: {}\n")
	}))
	defer srv.Close()
	_, err := NewLoader(t.TempDir(), false).Load(context.Background(), Ref{Repo: srv.URL, Chart: "web", Version: "1.0.0"})
	if hits.Load() != 2 || err == nil || strings.Contains(err.Error(), "504") {
		t.Errorf("a 504 is tried again: %d requests, %v", hits.Load(), err)
	}
}

func TestIndexThatPointsAtARegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "apiVersion: v1\nentries:\n  web:\n    - name: web\n      version: 1.0.0\n      urls: [\"oci://127.0.0.1:1/charts/web:1.0.0\"]\n")
	}))
	defer srv.Close()
	_, err := NewLoader(t.TempDir(), false).Load(context.Background(), Ref{Repo: srv.URL, Chart: "web", Version: "1.0.0"})
	if err == nil || strings.Contains(err.Error(), "unsupported protocol") || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("the chart must be pulled from the registry: %v", err)
	}
}

func TestConstraint(t *testing.T) {
	for _, tc := range []struct {
		version string
		isRange bool
	}{
		{"1.2.3", false},
		{"v1.2.3", false},
		{"latest", false},
		{"1.x", true},
		{">=1.0.0", true},
		{">=1.0.0 <2.0.0", true},
		{"*", true},
	} {
		if _, ok := constraint(tc.version); ok != tc.isRange {
			t.Errorf("constraint(%q) is a range = %v, want %v", tc.version, ok, tc.isRange)
		}
	}
}

func TestHighest(t *testing.T) {
	tags := []string{"0.9.0", "1.0.0", "1.4.2", "1.10.0", "2.0.0", "2.1.0-rc.1", "latest", "sha-abc"}
	for _, tc := range []struct {
		version, want string
		found         bool
	}{
		{"1.x", "1.10.0", true},
		{">=1.0.0 <2.0.0", "1.10.0", true},
		{"*", "2.0.0", true},
		{"~1.4.0", "1.4.2", true},
		{">=3.0.0", "", false},
	} {
		c, ok := constraint(tc.version)
		if !ok {
			t.Fatalf("constraint(%q) is not a range", tc.version)
		}
		got, found := highest(tags, c)
		if got != tc.want || found != tc.found {
			t.Errorf("highest(%q) = %q, %v; want %q, %v", tc.version, got, found, tc.want, tc.found)
		}
	}
}

func TestRefusedSaysWhatToCheck(t *testing.T) {
	ghcr := errors.New(`failed to perform "FetchReference" on source: GET "https://ghcr.io/token?scope=x": response status code 403: denied: requested access to the resource is denied`)
	for _, tt := range []struct {
		err   error
		where string
		oci   bool
		want  string
	}{
		{ghcr, "ghcr.io/home-operations/charts-mirror/trivy-operator", true, "not found, or private and no credentials (for a private registry, run `helm registry login ghcr.io`): failed to perform"},
		{errors.New(`failed to perform "FetchReference" on source: GET "https://ocharted.jory.dev/v2/x/headlamp/manifests/0.45.0": basic credential not found`), "ocharted.jory.dev/x/headlamp", true, "not found, or private and no credentials (for a private registry, run `helm registry login ocharted.jory.dev`): failed to perform"},
		{errors.New("GET https://charts.example.com/index.yaml: 401 Unauthorized"), "https://charts.example.com", false, "access denied (for a private repository, add it with `helm repo add`"},
		{errors.New(`GET "https://ghcr.io/v2/x/manifests/9.9.9": response status code 404: manifest unknown`), "ghcr.io/x", true, "not found, check the chart name and version: "},
		{errors.New("dial tcp 1.2.3.4:443: connect: connection refused"), "ghcr.io/x", true, "dial tcp"},
	} {
		if got := refused(tt.err, tt.where, tt.oci).Error(); !strings.HasPrefix(got, tt.want) {
			t.Errorf("refused(%q) = %q, want it to start with %q", tt.err, got, tt.want)
		}
	}
}

func TestPrivateRepositoryAsksForCredentials(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusUnauthorized: "add it with `helm repo add` and its username and password",
		http.StatusNotFound:     "check the chart name and version",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		_, err := NewLoader(t.TempDir(), false).Load(context.Background(), Ref{Repo: srv.URL, Chart: "web", Version: "1.0.0"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("status %d: %v, want %q", status, err, want)
		}
	}
}
