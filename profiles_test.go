package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// profilesFixture mimics a real dev-services.json but adds fields flowlog's
// serviceConfig does not model (extra_top_level, a service's "owner", a
// profile's "slack_channel"), so tests can confirm add/remove never drops
// them.
const profilesFixture = `{
  "services_root": "$HOME/repos",
  "extra_top_level": "keep me",
  "services": [
    {"name": "collect_service", "alias": "Collect", "command": "node app.js", "description": "Collect", "owner": "team-a"},
    {"name": "billing_service", "alias": "Billing", "command": "node app.js", "description": "Billing"}
  ],
  "profiles": {
    "payment": {"description": "Core payment flow", "services": ["collect_service"], "slack_channel": "#payments"}
  }
}`

// setUpProfilesConfig writes profilesFixture to a temp file, points
// FLOWLOG_CONFIG at it for the duration of the test, and returns the path.
func setUpProfilesConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dev-services.json")
	if err := os.WriteFile(path, []byte(profilesFixture), 0644); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	t.Setenv("FLOWLOG_CONFIG", path)
	return path
}

func readProfiles(t *testing.T, path string) map[string]struct {
	Description string   `json:"description"`
	Services    []string `json:"services"`
} {
	t.Helper()
	cfg, err := loadRawConfig(path)
	if err != nil {
		t.Fatalf("loadRawConfig: %v", err)
	}
	return cfg.Profiles
}

func TestRunProfilesAddNewProfile(t *testing.T) {
	path := setUpProfilesConfig(t)

	if code := runProfilesAdd([]string{"checkout", "-s", "collect_service,billing_service", "-d", "Checkout flow"}); code != 0 {
		t.Fatalf("runProfilesAdd = %d, want 0", code)
	}

	profiles := readProfiles(t, path)
	p, ok := profiles["checkout"]
	if !ok {
		t.Fatalf("profile 'checkout' not found after add")
	}
	if p.Description != "Checkout flow" {
		t.Fatalf("description = %q, want %q", p.Description, "Checkout flow")
	}
	want := []string{"collect_service", "billing_service"}
	if len(p.Services) != len(want) || p.Services[0] != want[0] || p.Services[1] != want[1] {
		t.Fatalf("services = %v, want %v", p.Services, want)
	}
}

func TestRunProfilesAddAliasResolution(t *testing.T) {
	path := setUpProfilesConfig(t)

	if code := runProfilesAdd([]string{"aliases", "-s", "Collect,Billing"}); code != 0 {
		t.Fatalf("runProfilesAdd = %d, want 0", code)
	}

	profiles := readProfiles(t, path)
	p, ok := profiles["aliases"]
	if !ok {
		t.Fatalf("profile 'aliases' not found after add")
	}
	want := []string{"collect_service", "billing_service"}
	if len(p.Services) != len(want) || p.Services[0] != want[0] || p.Services[1] != want[1] {
		t.Fatalf("services = %v, want canonical names %v", p.Services, want)
	}
}

func TestRunProfilesAddDuplicateWithoutForce(t *testing.T) {
	path := setUpProfilesConfig(t)

	if code := runProfilesAdd([]string{"payment", "-s", "billing_service"}); code == 0 {
		t.Fatalf("runProfilesAdd = 0, want non-zero for duplicate profile without --force")
	}

	profiles := readProfiles(t, path)
	if got := profiles["payment"].Services; len(got) != 1 || got[0] != "collect_service" {
		t.Fatalf("existing profile mutated: services = %v, want unchanged [collect_service]", got)
	}
}

func TestRunProfilesAddDuplicateWithForce(t *testing.T) {
	path := setUpProfilesConfig(t)

	if code := runProfilesAdd([]string{"payment", "-s", "billing_service", "--force"}); code != 0 {
		t.Fatalf("runProfilesAdd = %d, want 0 with --force", code)
	}

	profiles := readProfiles(t, path)
	got := profiles["payment"].Services
	if len(got) != 1 || got[0] != "billing_service" {
		t.Fatalf("services = %v, want [billing_service] after --force overwrite", got)
	}
}

func TestRunProfilesAddUnknownService(t *testing.T) {
	path := setUpProfilesConfig(t)

	if code := runProfilesAdd([]string{"broken", "-s", "does_not_exist"}); code == 0 {
		t.Fatalf("runProfilesAdd = 0, want non-zero for unknown service")
	}

	profiles := readProfiles(t, path)
	if _, ok := profiles["broken"]; ok {
		t.Fatalf("profile 'broken' should not have been saved when a service is unknown")
	}
}

func TestRunProfilesRemoveExisting(t *testing.T) {
	path := setUpProfilesConfig(t)

	if code := runProfilesRemove([]string{"payment"}); code != 0 {
		t.Fatalf("runProfilesRemove = %d, want 0", code)
	}

	profiles := readProfiles(t, path)
	if _, ok := profiles["payment"]; ok {
		t.Fatalf("profile 'payment' still present after remove")
	}
}

func TestRunProfilesRemoveMissing(t *testing.T) {
	setUpProfilesConfig(t)

	if code := runProfilesRemove([]string{"does-not-exist"}); code == 0 {
		t.Fatalf("runProfilesRemove = 0, want non-zero for missing profile")
	}
}

// TestProfilesAddRemovePreserveUnknownFields exercises add then remove and
// checks that fields flowlog does not model (top-level, per-service,
// per-profile) survive both operations untouched.
func TestProfilesAddRemovePreserveUnknownFields(t *testing.T) {
	path := setUpProfilesConfig(t)

	if code := runProfilesAdd([]string{"checkout", "-s", "collect_service"}); code != 0 {
		t.Fatalf("runProfilesAdd = %d, want 0", code)
	}
	if code := runProfilesRemove([]string{"checkout"}); code != 0 {
		t.Fatalf("runProfilesRemove = %d, want 0", code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var extra string
	if err := json.Unmarshal(raw["extra_top_level"], &extra); err != nil || extra != "keep me" {
		t.Fatalf("extra_top_level = %q, %v, want %q, nil", extra, err, "keep me")
	}

	var services []map[string]any
	if err := json.Unmarshal(raw["services"], &services); err != nil {
		t.Fatalf("unmarshal services: %v", err)
	}
	if services[0]["owner"] != "team-a" {
		t.Fatalf("service owner = %v, want team-a", services[0]["owner"])
	}

	var profiles map[string]json.RawMessage
	if err := json.Unmarshal(raw["profiles"], &profiles); err != nil {
		t.Fatalf("unmarshal profiles: %v", err)
	}
	var payment map[string]any
	if err := json.Unmarshal(profiles["payment"], &payment); err != nil {
		t.Fatalf("unmarshal payment: %v", err)
	}
	if payment["slack_channel"] != "#payments" {
		t.Fatalf("payment.slack_channel = %v, want #payments", payment["slack_channel"])
	}
}

func TestValidateProfileName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "ok", input: "checkout", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "internal whitespace", input: "check out", wantErr: true},
		{name: "leading whitespace", input: " checkout", wantErr: true},
		{name: "trailing whitespace", input: "checkout ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProfileName(tt.input)
			if tt.wantErr && err == nil {
				t.Fatalf("validateProfileName(%q) = nil, want error", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateProfileName(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}
