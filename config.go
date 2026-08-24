package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type serviceConfig struct {
	ServicesRoot string `json:"services_root"`
	Profiles     map[string]struct {
		Description string   `json:"description"`
		Services    []string `json:"services"`
	} `json:"profiles"`
	Services []struct {
		Name        string            `json:"name"`
		Alias       string            `json:"alias"`
		Command     string            `json:"command"`
		Description string            `json:"description"`
		Env         map[string]string `json:"env"`
	} `json:"services"`
}

// loadRawConfig reads and parses ~/dev-services.json without projecting services
// down to the tail-only Service type; `start` needs the command and env fields
// loadServices drops.
func loadRawConfig(configPath string) (*serviceConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var cfg serviceConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if strings.HasPrefix(cfg.ServicesRoot, "$HOME") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		cfg.ServicesRoot = filepath.Join(home, strings.TrimPrefix(cfg.ServicesRoot, "$HOME"))
	}
	return &cfg, nil
}

func loadServices(configPath string, profile string, only []string) ([]Service, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var cfg serviceConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	root := cfg.ServicesRoot
	if strings.HasPrefix(root, "$HOME") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(home, strings.TrimPrefix(root, "$HOME"))
	}
	resolve := func(name string, alias string) Service {
		if alias == "" {
			alias = name
		}
		return Service{Name: name, Alias: alias, Path: filepath.Join(root, name)}
	}
	all := make([]Service, 0, len(cfg.Services))
	byName := make(map[string]Service, len(cfg.Services))
	for _, s := range cfg.Services {
		svc := resolve(s.Name, s.Alias)
		all = append(all, svc)
		byName[s.Name] = svc
	}
	var selected []Service
	if profile == "" {
		selected = all
	} else {
		p, ok := cfg.Profiles[profile]
		if !ok {
			return nil, fmt.Errorf("unknown profile: %s", profile)
		}
		for _, name := range p.Services {
			svc, ok := byName[name]
			if !ok {
				svc = resolve(name, "")
			}
			selected = append(selected, svc)
		}
	}
	if len(only) == 0 {
		return selected, nil
	}
	want := make(map[string]bool, len(only))
	for _, o := range only {
		want[strings.ToLower(o)] = true
	}
	matched := make(map[string]bool, len(only))
	var filtered []Service
	for _, svc := range selected {
		name := strings.ToLower(svc.Name)
		alias := strings.ToLower(svc.Alias)
		hit := false
		if want[name] {
			matched[name] = true
			hit = true
		}
		if want[alias] {
			matched[alias] = true
			hit = true
		}
		if hit {
			filtered = append(filtered, svc)
		}
	}
	var unknown []string
	for _, o := range only {
		if !matched[strings.ToLower(o)] {
			unknown = append(unknown, o)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown services: %s", strings.Join(unknown, ", "))
	}
	return filtered, nil
}

// defaultConfigPath is the config file flowlog uses when the caller has not
// been given one explicitly: $FLOWLOG_CONFIG if set, else ~/dev-services.json.
func defaultConfigPath() (string, error) {
	if p := os.Getenv("FLOWLOG_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "dev-services.json"), nil
}

// configMap is ~/dev-services.json decoded one JSON level deep. serviceConfig
// only models the fields flowlog reads; round-tripping a config through it to
// make a small edit (add/remove a profile) would silently drop any other
// field the user has in the file. configMap keeps every field as raw JSON so
// a write only ever touches the keys it means to.
type configMap map[string]json.RawMessage

func loadConfigMap(path string) (configMap, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg configMap
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// saveConfigMap writes cfg to path as indented JSON, atomically: it writes to
// a temp file in the same directory and renames it over path, so a reader
// never observes a partial write. The file keeps its original mode, or 0644
// if path does not exist yet.
func saveConfigMap(path string, cfg configMap) error {
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dev-services-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
