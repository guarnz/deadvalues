package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOutputFileReplacesStdout(t *testing.T) {
	file := filepath.Join(t.TempDir(), "report.md")
	out, code := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "-o", "markdown", "--output-file", file)
	if code != 1 {
		t.Errorf("exit code must not change when writing to a file: %d", code)
	}
	if out != "" {
		t.Errorf("stdout should be empty, got:\n%s", out)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "| **DEAD** | `service.prot`") || !strings.Contains(string(data), "` — cases") {
		t.Errorf("file must hold the UTF-8 markdown report:\n%s", data)
	}
	if strings.Contains(string(data), "\x1b[") {
		t.Error("files are never colored")
	}
}

func TestOutputFileSeveralFormatsInOneRun(t *testing.T) {
	dir := t.TempDir()
	md, sarif := filepath.Join(dir, "report.md"), filepath.Join(dir, "results.sarif")
	out, code := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "--color", "always",
		"--output-file", "markdown="+md, "--output-file", "sarif="+sarif)
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(out, "service.prot") || !strings.Contains(out, "\x1b[") {
		t.Errorf("FORMAT=FILE keeps the colored table on stdout:\n%s", out)
	}
	if data, _ := os.ReadFile(md); !strings.Contains(string(data), "| Status | Key |") {
		t.Errorf("markdown file:\n%s", data)
	}
	data, _ := os.ReadFile(sarif)
	var doc struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []struct {
				RuleID string `json:"ruleId"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != "2.1.0" || len(doc.Runs[0].Results) == 0 {
		t.Errorf("sarif file (%v):\n%s", err, data)
	}
}

func TestReportFiles(t *testing.T) {
	chart, values := absPath(t, casesChart), absPath(t, casesValues)
	check := []string{"check", "--chart", chart, "-f", values}
	day := time.Now().Format("2006-01-02")
	dir := t.TempDir()
	t.Chdir(dir)

	out, code := runCLI(t, append(check, "--report")...)
	if code != 1 || !strings.Contains(out, "service.prot") {
		t.Errorf("--report keeps the table on stdout and the exit code: %d\n%s", code, out)
	}
	for _, name := range []string{"check-" + day + ".json", "check-" + day + ".md", "check-" + day + ".sarif"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("--report without a file format in -o writes all of them: %v", err)
		}
	}

	runCLI(t, append(check, "-o", "table,json", "--report-path", "reports/")...)
	if entries, _ := os.ReadDir(filepath.Join(dir, "reports")); len(entries) != 1 || entries[0].Name() != "check-"+day+".json" {
		t.Errorf("a directory gets the -o file formats with the default name, got %v", entries)
	}

	runCLI(t, append(check, "-o", "json,sarif", "--report-path", "out/home")...)
	for _, name := range []string{"home.json", "home.sarif"} {
		if _, err := os.Stat(filepath.Join(dir, "out", name)); err != nil {
			t.Errorf("a base name gets the extension of each format: %v", err)
		}
	}

	runCLI(t, append(check, "--report-path", "results.sarif")...)
	if data, _ := os.ReadFile(filepath.Join(dir, "results.sarif")); !strings.Contains(string(data), `"version": "2.1.0"`) {
		t.Errorf("a file name with a format's extension is that file:\n%s", data)
	}

	for _, args := range [][]string{
		{"-o", "json,sarif"},
		{"-o", "json,sarif", "--report-path", "x.json"},
		{"-o", "table,github", "--report"},
	} {
		if out, code := runCLI(t, append(check, args...)...); code != 2 {
			t.Errorf("%v: exit %d, want 2\n%s", args, code, out)
		}
	}
	if _, code := runCLI(t, "prune", "-f", values, "--chart", chart, "--report"); code != 2 {
		t.Errorf("prune writes no report: exit %d, want 2", code)
	}
}

func absPath(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestJSONReportEnvelope(t *testing.T) {
	type envelope struct {
		SchemaVersion int               `json:"schemaVersion"`
		Deadvalues    string            `json:"deadvalues"`
		Command       string            `json:"command"`
		GeneratedAt   string            `json:"generatedAt"`
		Results       []json.RawMessage `json:"results"`
	}
	decode := func(data string) envelope {
		t.Helper()
		var e envelope
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatalf("%v\n%s", err, data)
		}
		if _, err := time.Parse(time.RFC3339, e.GeneratedAt); err != nil || e.SchemaVersion != 1 || e.Deadvalues != "test" {
			t.Errorf("envelope (%v):\n%s", err, data)
		}
		return e
	}
	file := filepath.Join(t.TempDir(), "report.json")
	out, _ := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "-o", "json", "--output-file", "json="+file)
	e := decode(out)
	if e.Command != "check" || len(e.Results) != 1 {
		t.Errorf("one app is still a list of one result, got %q with %d", e.Command, len(e.Results))
	}
	data, _ := os.ReadFile(file)
	if f := decode(string(data)); f.GeneratedAt != e.GeneratedAt {
		t.Errorf("stdout and the file come from the same run: %s, %s", e.GeneratedAt, f.GeneratedAt)
	}
	out, _ = runCLI(t, "explain", "service.prot", "--chart", casesChart, "-f", casesValues, "-o", "json")
	if e := decode(out); e.Command != "explain" || len(e.Results) != 1 {
		t.Errorf("explain: %q with %d results", e.Command, len(e.Results))
	}
}

func TestOutputFileErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "report.md")
	if _, code := runCLI(t, "check", "--chart", casesChart, "-f", casesValues, "--output-file", missing); code != 2 {
		t.Errorf("missing directory: exit %d, want 2", code)
	}
	file := filepath.Join(t.TempDir(), "x")
	for _, args := range [][]string{
		{"prune", "--chart", casesChart, "-f", casesValues, "--dry-run", "--output-file", file},
		{"version", "--output-file", file},
	} {
		if _, code := runCLI(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args[0], code)
		}
	}
}
