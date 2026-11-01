package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guarnz/deadvalues/internal/git"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	args = append([]string{"-c", "user.email=a@b", "-c", "user.name=a", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)
	out, err := git.Command(dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const gitChartYAML = "apiVersion: v2\nname: web\nversion: %s\n"

// chartRepo makes a repository with versions 1.0.0 and 1.2.0 of a chart, the first
// under a lightweight tag and the second under an annotated one, and main ahead of both.
func chartRepo(t *testing.T) (url string, commits map[string]string) {
	t.Helper()
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	commits = map[string]string{}
	for _, v := range []string{"1.0.0", "1.2.0", "2.0.0-dev"} {
		writeTree(t, dir, map[string]string{"charts/web/Chart.yaml": strings.Replace(gitChartYAML, "%s", v, 1), "envs/web.yaml": "version: " + v + "\n"})
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", v)
		commits[v] = gitIn(t, dir, "rev-parse", "HEAD")
	}
	gitIn(t, dir, "tag", "v1.0.0", commits["1.0.0"])
	gitIn(t, dir, "tag", "-a", "v1.2.0", "-m", "x", commits["1.2.0"])
	return "file://" + filepath.ToSlash(dir), commits
}

func TestChartFromGitAtEveryKindOfRevision(t *testing.T) {
	url, commits := chartRepo(t)
	for rev, want := range map[string]string{
		"":                   "2.0.0-dev",
		"HEAD":               "2.0.0-dev",
		"main":               "2.0.0-dev",
		"v1.0.0":             "1.0.0",
		"v1.2.0":             "1.2.0",
		"refs/tags/v1.2.0":   "1.2.0",
		"refs/heads/main":    "2.0.0-dev",
		"1.x":                "1.2.0",
		">=1.0.0 <1.2.0":     "1.0.0",
		commits["1.0.0"]:     "1.0.0",
		commits["2.0.0-dev"]: "2.0.0-dev",
	} {
		t.Run(rev, func(t *testing.T) {
			l := NewLoader(t.TempDir(), false)
			c, err := l.Load(context.Background(), Ref{Git: url, Chart: "./charts/web", Version: rev})
			if err != nil {
				t.Fatal(err)
			}
			if c.Metadata.Version != want {
				t.Errorf("version = %s, want %s", c.Metadata.Version, want)
			}
		})
	}
}

func TestCheckoutIsCachedByCommit(t *testing.T) {
	url, commits := chartRepo(t)
	l := NewLoader(t.TempDir(), false)
	ctx := context.Background()
	dir, err := l.Checkout(ctx, url, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != commits["1.0.0"] {
		t.Errorf("dir = %s", dir)
	}
	marker := filepath.Join(dir, "marker")
	writeTree(t, dir, map[string]string{"marker": ""})
	again, err := NewLoader(l.CacheDir, false).Checkout(ctx, url, commits["1.0.0"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil || again != dir {
		t.Errorf("a commit in the cache must not be fetched again: %s, %v", again, err)
	}
	if _, err := NewLoader(l.CacheDir, true).Checkout(ctx, url, commits["1.0.0"]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("--no-cache must fetch again")
	}
}

func TestChartFromGitThatCannotBeFetchedIsUnavailable(t *testing.T) {
	url, _ := chartRepo(t)
	for name, ref := range map[string]Ref{
		"no repository": {Git: url + "-missing", Chart: "charts/web"},
		"no revision":   {Git: url, Chart: "charts/web", Version: "nope"},
		"no range":      {Git: url, Chart: "charts/web", Version: ">=9.0.0"},
		"no chart":      {Git: url, Chart: "charts/nope"},
		"outside":       {Git: url, Chart: "../../etc"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewLoader(t.TempDir(), false).Load(context.Background(), ref)
			reason, ok := Unavailable(err)
			if !ok || !strings.Contains(reason, "give git access") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestGitDoesNotRunWhatAURLAsks(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "")
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := NewLoader(t.TempDir(), false).Checkout(context.Background(), "ext::sh -c touch% "+filepath.ToSlash(marker), "main")
	if err == nil {
		t.Error("an ext:: URL must be refused")
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the URL ran a command")
	}
}

func TestSymbolicLinksOfACloneStayFiles(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	secret := filepath.Join(t.TempDir(), "secret")
	writeTree(t, secret, map[string]string{"key": "hunter2"})
	if err := os.Symlink(filepath.Join(secret, "key"), filepath.Join(dir, "values.yaml")); err != nil {
		t.Skip("no symbolic links here")
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "link")
	clone, err := NewLoader(t.TempDir(), false).Checkout(context.Background(), "file://"+filepath.ToSlash(dir), "main")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(clone, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hunter2") {
		t.Error("the link was followed")
	}
}

func TestChartFromGitGetsItsDependencies(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeTree(t, dir, map[string]string{
		"charts/app/Chart.yaml":                 "apiVersion: v2\nname: app\nversion: 1.0.0\ndependencies:\n  - name: lib\n    version: 1.0.0\n    repository: file://../lib\n  - name: vendored\n    version: 1.0.0\n    repository: https://example.invalid\n",
		"charts/app/charts/vendored/Chart.yaml": "apiVersion: v2\nname: vendored\nversion: 1.0.0\n",
		"charts/lib/Chart.yaml":                 "apiVersion: v2\nname: lib\nversion: 1.0.0\n",
		"charts/loop/Chart.yaml":                "apiVersion: v2\nname: loop\nversion: 1.0.0\ndependencies:\n  - name: loop2\n    version: 1.0.0\n    repository: file://../loop2\n",
		"charts/loop2/Chart.yaml":               "apiVersion: v2\nname: loop2\nversion: 1.0.0\ndependencies:\n  - name: loop\n    version: 1.0.0\n    repository: file://../loop\n",
		"charts/away/Chart.yaml":                "apiVersion: v2\nname: away\nversion: 1.0.0\ndependencies:\n  - name: etc\n    version: 1.0.0\n    repository: file://../../../etc\n",
	})
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "charts")
	url := "file://" + filepath.ToSlash(dir)
	l := NewLoader(t.TempDir(), false)
	c, err := l.Load(context.Background(), Ref{Git: url, Chart: "charts/app"})
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
	for _, chart := range []string{"charts/loop", "charts/away"} {
		if _, err := l.Load(context.Background(), Ref{Git: url, Chart: chart}); err == nil {
			t.Errorf("%s must not load", chart)
		}
	}
}

func TestRefOfAChartFromGit(t *testing.T) {
	r := Ref{Git: "https://github.com/acme/charts", Chart: "charts/web", Version: "v1"}
	if r.IsLocal() {
		t.Error("a chart from git is not local")
	}
	if got := r.String(); got != "https://github.com/acme/charts/charts/web@v1" {
		t.Errorf("String = %s", got)
	}
	got, err := NewLoader(t.TempDir(), false).ResolveAlias(r)
	if err != nil || got != r {
		t.Errorf("ResolveAlias = %+v, %v", got, err)
	}
}
