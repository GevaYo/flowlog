package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The index template is embedded so `flowlog setup` works from any directory,
// without needing to locate the repo's opensearch/template.json.
//
//go:embed opensearch/template.json
var indexTemplateJSON []byte

const wantSampleSize = 5000

// checkEnv is what the checks run against.
type checkEnv struct {
	osURL      string
	osdURL     string
	prefix     string
	configPath string
	client     *http.Client
}

func (e *checkEnv) dailyIndex() string { return e.prefix + "-" + time.Now().Format("2006.01.02") }

// checkResult is one check's verdict. hint is the manual remedy, printed when
// the check fails and nothing repaired it.
type checkResult struct {
	ok     bool
	detail string
	hint   string
}

func pass(format string, args ...any) checkResult {
	return checkResult{ok: true, detail: fmt.Sprintf(format, args...)}
}

func fail(hint string, format string, args ...any) checkResult {
	return checkResult{detail: fmt.Sprintf(format, args...), hint: hint}
}

// check is one diagnosable condition. fix is nil when only the user can
// resolve it (a service that is not running, a config file to author). dep
// names a check that must pass first; if it did not, this one is skipped
// rather than reported as a second failure with the same root cause.
type check struct {
	name string
	dep  string
	run  func(*checkEnv) checkResult
	fix  func(*checkEnv) error
}

// checks is the single source of truth for both subcommands: `doctor` runs
// them read-only, `setup` runs the same list and applies every available fix.
// Keeping one list is deliberate - two lists drift, and then doctor starts
// passing things setup no longer does.
var checks = []check{
	{
		name: "dev-services.json",
		run: func(e *checkEnv) checkResult {
			svcs, err := loadServices(e.configPath, "", nil)
			if err != nil {
				return fail("create "+e.configPath+" (copy dev-services.json from the dev-services repo)", "%v", err)
			}
			if len(svcs) == 0 {
				return fail("add a \"services\" array to "+e.configPath, "no services defined")
			}
			return pass("%d services", len(svcs))
		},
	},
	{
		name: "config profiles",
		dep:  "dev-services.json",
		run: func(e *checkEnv) checkResult {
			names, err := configProfiles(e.configPath)
			if err != nil {
				return fail("check the JSON in "+e.configPath, "%v", err)
			}
			if len(names) == 0 {
				return fail("add a \"profiles\" object to "+e.configPath,
					"none defined - dev-services start -p and the flowlog logs window both need one")
			}
			return pass("%s", strings.Join(names, ", "))
		},
	},
	{
		name: "tmux installed",
		run: func(e *checkEnv) checkResult {
			path, err := exec.LookPath("tmux")
			if err != nil {
				return fail("brew install tmux", "not found in PATH")
			}
			return pass("%s", path)
		},
	},
	{
		name: "aws cli + SSO profile",
		run: func(e *checkEnv) checkResult {
			path, err := exec.LookPath("aws")
			if err != nil {
				return fail("brew install awscli", "not found in PATH")
			}
			out, err := exec.Command("aws", "configure", "list-profiles").Output()
			if err != nil || !strings.Contains(string(out), awsProfile) {
				return fail(fmt.Sprintf("configure the %s SSO profile (aws configure sso --profile %s)", awsProfile, awsProfile), "profile %q not found", awsProfile)
			}
			return pass("%s, %s configured", path, awsProfile)
		},
	},
	{
		// Compares the installed completion file against what this binary
		// emits, so doctor catches the "upgraded flowlog, stale completions"
		// drift. The fpath line is a one-time manual step: editing ~/.zshrc
		// automatically is not setup's call to make.
		name: "zsh completions",
		run: func(e *checkEnv) checkResult {
			if !strings.Contains(os.Getenv("SHELL"), "zsh") {
				return pass("shell is not zsh, nothing to do")
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fail("", "%v", err)
			}
			path := filepath.Join(home, ".zfunc", "_flowlog")
			data, err := os.ReadFile(path)
			if err != nil {
				return fail("flowlog setup", "not installed (%s missing)", path)
			}
			if string(data) != zshCompletion {
				return fail("flowlog setup", "out of date with this binary")
			}
			zshrc, _ := os.ReadFile(filepath.Join(home, ".zshrc"))
			if !strings.Contains(string(zshrc), ".zfunc") {
				return fail("add fpath=(~/.zfunc $fpath) to ~/.zshrc before compinit",
					"completion file installed but ~/.zfunc is not in fpath")
			}
			return pass("installed and current")
		},
		fix: func(e *checkEnv) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			dir := filepath.Join(home, ".zfunc")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "_flowlog"), []byte(zshCompletion), 0o644)
		},
	},
	{
		name: "opensearch reachable",
		run: func(e *checkEnv) checkResult {
			code, body, err := e.request("GET", e.osURL+"/_cluster/health", "", nil)
			if err != nil {
				return fail("brew services start opensearch  (first install: see opensearch/setup.md steps 1-2)", "%v", err)
			}
			if code != http.StatusOK {
				return fail("brew services start opensearch", "status %d", code)
			}
			var h struct {
				Status string `json:"status"`
			}
			json.Unmarshal(body, &h)
			if h.Status != "yellow" && h.Status != "green" {
				return fail("check the OpenSearch log; cluster is not serving", "cluster status %q", h.Status)
			}
			return pass("cluster %s", h.Status)
		},
	},
	{
		name: "index template",
		dep:  "opensearch reachable",
		run: func(e *checkEnv) checkResult {
			code, _, err := e.request("GET", e.osURL+"/_index_template/"+e.prefix, "", nil)
			if err != nil || code != http.StatusOK {
				return fail("flowlog setup", "template %q missing", e.prefix)
			}
			return pass("%q applied", e.prefix)
		},
		fix: func(e *checkEnv) error {
			return e.expect("PUT", e.osURL+"/_index_template/"+e.prefix, "application/json", indexTemplateJSON)
		},
	},
	{
		// The index pattern below needs an index with mappings to build its
		// field cache, but it does not need one with documents. Creating
		// today's index empty is what lets setup finish in a single pass
		// instead of "ship some logs, then come back".
		name: "today's daily index",
		dep:  "index template",
		run: func(e *checkEnv) checkResult {
			code, _, err := e.request("GET", e.osURL+"/"+e.dailyIndex(), "", nil)
			if err != nil || code != http.StatusOK {
				return fail("flowlog setup", "%s missing", e.dailyIndex())
			}
			return pass("%s", e.dailyIndex())
		},
		fix: func(e *checkEnv) error {
			return e.expect("PUT", e.osURL+"/"+e.dailyIndex(), "", nil)
		},
	},
	{
		name: "dashboards reachable",
		run: func(e *checkEnv) checkResult {
			code, _, err := e.request("GET", e.osdURL+"/api/status", "", nil)
			if err != nil {
				return fail("mkdir -p $(brew --prefix)/var/run && brew services start opensearch-dashboards", "%v", err)
			}
			if code != http.StatusOK {
				return fail("wait for first boot (can take a minute), then re-run", "status %d", code)
			}
			return pass("responding")
		},
	},
	{
		// Created without a populated field cache, the pattern looks fine but
		// Discover silently returns nothing and the histogram fails with
		// "Could not locate that index-pattern-field (id: time)". So the
		// cache, not just the object, is what gets checked.
		name: "index pattern field cache",
		dep:  "dashboards reachable",
		run: func(e *checkEnv) checkResult {
			code, body, err := e.request("GET", e.osdURL+"/api/saved_objects/index-pattern/"+e.prefix, "", nil)
			if err != nil {
				return fail("flowlog setup", "%v", err)
			}
			if code != http.StatusOK {
				return fail("flowlog setup", "pattern %q* missing", e.prefix)
			}
			var obj struct {
				Attributes struct {
					Fields string `json:"fields"`
				} `json:"attributes"`
			}
			if json.Unmarshal(body, &obj) != nil {
				return fail("flowlog setup", "unreadable saved object")
			}
			var fields []struct {
				Name string `json:"name"`
			}
			json.Unmarshal([]byte(obj.Attributes.Fields), &fields)
			hasTime := false
			for _, f := range fields {
				if f.Name == "time" {
					hasTime = true
					break
				}
			}
			if !hasTime {
				return fail("flowlog setup", "field cache empty or missing the time field (Discover would show no results)")
			}
			return pass("%d fields cached", len(fields))
		},
		fix: func(e *checkEnv) error { return e.rebuildIndexPattern() },
	},
	{
		name: "saved search",
		dep:  "index pattern field cache",
		run: func(e *checkEnv) checkResult {
			code, _, err := e.request("GET", e.osdURL+"/api/saved_objects/search/"+e.prefix+"-flow", "", nil)
			if err != nil || code != http.StatusOK {
				return fail("flowlog setup", "saved search %q missing", e.prefix)
			}
			return pass("%q", e.prefix)
		},
		fix: func(e *checkEnv) error { return e.createSavedSearch() },
	},
	{
		name: "discover:sampleSize",
		dep:  "dashboards reachable",
		run: func(e *checkEnv) checkResult {
			code, body, err := e.request("GET", e.osdURL+"/api/opensearch-dashboards/settings", "", nil)
			if err != nil || code != http.StatusOK {
				return fail("flowlog setup", "cannot read Dashboards settings")
			}
			var s struct {
				Settings struct {
					SampleSize struct {
						UserValue int `json:"userValue"`
					} `json:"discover:sampleSize"`
				} `json:"settings"`
			}
			json.Unmarshal(body, &s)
			if got := s.Settings.SampleSize.UserValue; got < wantSampleSize {
				return fail("flowlog setup", "%d, want >= %d", got, wantSampleSize)
			}
			return pass("%d", s.Settings.SampleSize.UserValue)
		},
		fix: func(e *checkEnv) error {
			return e.osdWrite("/api/opensearch-dashboards/settings",
				map[string]any{"changes": map[string]any{"discover:sampleSize": wantSampleSize}})
		},
	},
}

// runChecks powers both subcommands. apply=false is doctor, true is setup.
func runChecks(args []string, apply bool) int {
	name := "doctor"
	if apply {
		name = "setup"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	osURL := fs.String("os-url", "http://localhost:9200", "OpenSearch base URL")
	osdURL := fs.String("osd-url", "http://localhost:5601", "OpenSearch Dashboards base URL")
	index := fs.String("index", "flowlog", "index prefix / saved object id")
	fs.Parse(args)

	configPath, err := defaultConfigPath()
	if err != nil {
		fatalf("cannot resolve home directory: %v", err)
	}
	e := &checkEnv{
		osURL:      strings.TrimSuffix(*osURL, "/"),
		osdURL:     strings.TrimSuffix(*osdURL, "/"),
		prefix:     *index,
		configPath: configPath,
		client:     &http.Client{Timeout: 30 * time.Second},
	}

	color := isTerminal(os.Stdout)
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return code + s + reset
	}
	// Pad before coloring: ANSI codes are bytes, so %-5s cannot align them.
	status := func(code, label string) string { return paint(code, fmt.Sprintf("%-5s", label)) }

	fmt.Printf("\nflowlog %s  %s\n\n", name, paint(dim, e.osURL+"  "+e.osdURL))

	failedNames := map[string]bool{}
	var failures, skipped, fixed int
	for _, c := range checks {
		if c.dep != "" && failedNames[c.dep] {
			failedNames[c.name] = true
			skipped++
			fmt.Printf("  %s  %-26s %s\n", status(dim, "skip"), c.name, paint(dim, "needs "+c.dep))
			continue
		}
		r := c.run(e)
		wasFixed := false
		if !r.ok && apply && c.fix != nil {
			if err := c.fix(e); err != nil {
				r.detail = fmt.Sprintf("%s (fix failed: %v)", r.detail, err)
			} else if again := c.run(e); again.ok {
				r, wasFixed = again, true
			} else {
				r = again
			}
		}
		switch {
		case r.ok && wasFixed:
			fixed++
			fmt.Printf("  %s  %-26s %s\n", status("\x1b[32m", "fixed"), c.name, paint(dim, r.detail))
		case r.ok:
			fmt.Printf("  %s  %-26s %s\n", status("\x1b[32m", "ok"), c.name, paint(dim, r.detail))
		default:
			failures++
			failedNames[c.name] = true
			fmt.Printf("  %s  %-26s %s\n", status("\x1b[31m", "fail"), c.name, r.detail)
			if r.hint != "" {
				fmt.Printf("         %s %s\n", paint(dim, "fix:"), r.hint)
			}
		}
	}

	fmt.Println()
	switch {
	case failures > 0 && apply:
		fmt.Printf("%d still failing after fixes", failures)
		if skipped > 0 {
			fmt.Printf(", %d skipped", skipped)
		}
		fmt.Println(". Resolve the items above and re-run.")
	case failures > 0:
		fmt.Printf("%d failing", failures)
		if skipped > 0 {
			fmt.Printf(", %d skipped", skipped)
		}
		fmt.Println(". Run `flowlog setup` to repair what can be repaired automatically.")
	case fixed > 0:
		fmt.Printf("All good (%d repaired). Ship logs with: flowlog -f --profile <name> --ship\n", fixed)
	default:
		fmt.Println("All good. Ship logs with: flowlog -f --profile <name> --ship")
	}
	if failures > 0 {
		return 1
	}
	return 0
}

// configProfiles returns the profile names declared in the shared config.
func configProfiles(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg serviceConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	return names, nil
}

func (e *checkEnv) request(method, url, contentType string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("osd-xsrf", "true") // required by Dashboards writes, ignored by OpenSearch
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, nil
}

// expect performs a request and turns a non-2xx into an error.
func (e *checkEnv) expect(method, url, contentType string, body []byte) error {
	code, out, err := e.request(method, url, contentType, body)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("%s %s: status %d: %s", method, url, code, truncate(string(out), 200))
	}
	return nil
}

func (e *checkEnv) osdWrite(path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return e.expect("POST", e.osdURL+path, "application/json", body)
}

// rebuildIndexPattern recreates the pattern with its field cache populated
// from _fields_for_wildcard, which is the only form Discover works with.
func (e *checkEnv) rebuildIndexPattern() error {
	meta := "&meta_fields=_source&meta_fields=_id&meta_fields=_type&meta_fields=_index&meta_fields=_score"
	code, body, err := e.request("GET",
		e.osdURL+"/api/index_patterns/_fields_for_wildcard?pattern="+e.prefix+"*"+meta, "", nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("_fields_for_wildcard: status %d: %s", code, truncate(string(body), 200))
	}
	var wrap struct {
		Fields []map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return err
	}
	if len(wrap.Fields) == 0 {
		return fmt.Errorf("_fields_for_wildcard returned no fields; is the index template applied?")
	}
	fields, err := json.Marshal(wrap.Fields)
	if err != nil {
		return err
	}
	return e.osdWrite("/api/saved_objects/index-pattern/"+e.prefix+"?overwrite=true", map[string]any{
		"attributes": map[string]any{
			"title":         e.prefix + "*",
			"timeFieldName": "time",
			"fields":        string(fields),
		},
	})
}

// createSavedSearch reproduces the QA Discover layout so muscle memory carries
// over unchanged: service | level | message | timestamp_ns, newest first.
func (e *checkEnv) createSavedSearch() error {
	searchSource, err := json.Marshal(map[string]any{
		"query":         map[string]any{"query": "", "language": "kuery"},
		"filter":        []any{},
		"indexRefName": "kibanaSavedObjectMeta.searchSourceJSON.index",
	})
	if err != nil {
		return err
	}
	return e.osdWrite("/api/saved_objects/search/"+e.prefix+"-flow?overwrite=true", map[string]any{
		"attributes": map[string]any{
			"title":   e.prefix,
			"columns": []string{"service", "level", "message", "timestamp_ns"},
			"sort":    [][]string{{"time", "desc"}},
			"kibanaSavedObjectMeta": map[string]any{
				"searchSourceJSON": string(searchSource),
			},
		},
		"references": []map[string]string{{
			"id":   e.prefix,
			"name": "kibanaSavedObjectMeta.searchSourceJSON.index",
			"type": "index-pattern",
		}},
	})
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
