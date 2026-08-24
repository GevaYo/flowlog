package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixtureWithUnknownFields mimics a real dev-services.json but adds fields
// flowlog's serviceConfig does not model, at the top level, on a service, and
// on a profile. saveConfigMap must round-trip these untouched.
const fixtureWithUnknownFields = `{
  "services_root": "$HOME/repos",
  "extra_top_level": "keep me",
  "services": [
    {
      "name": "collect_service",
      "alias": "Collect",
      "command": "node app.js",
      "description": "Collect Service",
      "owner": "team-payments"
    }
  ],
  "profiles": {
    "payment": {
      "description": "Core payment flow",
      "services": ["collect_service"],
      "slack_channel": "#payments"
    }
  }
}`

func TestSaveConfigMapRoundTripPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev-services.json")
	if err := os.WriteFile(path, []byte(fixtureWithUnknownFields), 0644); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	cfg, err := loadConfigMap(path)
	if err != nil {
		t.Fatalf("loadConfigMap: %v", err)
	}
	if _, ok := cfg["extra_top_level"]; !ok {
		t.Fatalf("fixture missing expected top-level field before edit")
	}

	// Add a new profile, the way profiles.go would.
	var profiles map[string]json.RawMessage
	if err := json.Unmarshal(cfg["profiles"], &profiles); err != nil {
		t.Fatalf("unmarshal profiles: %v", err)
	}
	entry, err := json.Marshal(map[string]any{"description": "new one", "services": []string{"collect_service"}})
	if err != nil {
		t.Fatalf("marshal new profile: %v", err)
	}
	profiles["fresh"] = entry
	profilesJSON, err := json.Marshal(profiles)
	if err != nil {
		t.Fatalf("marshal profiles: %v", err)
	}
	cfg["profiles"] = profilesJSON

	if err := saveConfigMap(path, cfg); err != nil {
		t.Fatalf("saveConfigMap: %v", err)
	}

	// Re-parse from disk and check nothing was lost.
	reloaded, err := loadConfigMap(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	var extra string
	if err := json.Unmarshal(reloaded["extra_top_level"], &extra); err != nil || extra != "keep me" {
		t.Fatalf("extra_top_level = %q, %v, want %q, nil", extra, err, "keep me")
	}

	var services []map[string]any
	if err := json.Unmarshal(reloaded["services"], &services); err != nil {
		t.Fatalf("unmarshal services: %v", err)
	}
	if got := services[0]["owner"]; got != "team-payments" {
		t.Fatalf("service owner = %v, want team-payments", got)
	}

	var reloadedProfiles map[string]json.RawMessage
	if err := json.Unmarshal(reloaded["profiles"], &reloadedProfiles); err != nil {
		t.Fatalf("unmarshal profiles: %v", err)
	}
	var payment map[string]any
	if err := json.Unmarshal(reloadedProfiles["payment"], &payment); err != nil {
		t.Fatalf("unmarshal payment profile: %v", err)
	}
	if got := payment["slack_channel"]; got != "#payments" {
		t.Fatalf("payment.slack_channel = %v, want #payments", got)
	}
	if _, ok := reloadedProfiles["fresh"]; !ok {
		t.Fatalf("new profile %q not present after round trip", "fresh")
	}
}

func TestSaveConfigMapPreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev-services.json")
	if err := os.WriteFile(path, []byte(fixtureWithUnknownFields), 0600); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	cfg, err := loadConfigMap(path)
	if err != nil {
		t.Fatalf("loadConfigMap: %v", err)
	}
	if err := saveConfigMap(path, cfg); err != nil {
		t.Fatalf("saveConfigMap: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestDefaultConfigPathHonorsEnvVar(t *testing.T) {
	t.Setenv("FLOWLOG_CONFIG", "/tmp/some-config.json")
	got, err := defaultConfigPath()
	if err != nil {
		t.Fatalf("defaultConfigPath: %v", err)
	}
	if got != "/tmp/some-config.json" {
		t.Fatalf("defaultConfigPath = %q, want /tmp/some-config.json", got)
	}
}
