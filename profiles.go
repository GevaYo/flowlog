package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
)

func runProfiles(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "add":
			return runProfilesAdd(args[1:])
		case "remove":
			return runProfilesRemove(args[1:])
		}
	}

	fs := flag.NewFlagSet("profiles", flag.ExitOnError)
	fs.Parse(args)

	configPath, err := defaultConfigPath()
	if err != nil {
		fatalf("cannot resolve home directory: %v", err)
	}
	cfg, err := loadRawConfig(configPath)
	if err != nil {
		fatalf("%v", err)
	}

	if len(cfg.Profiles) == 0 {
		fmt.Println("No profiles defined in configuration.")
		return 0
	}

	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Println("Available profiles:")
	fmt.Println()
	for _, name := range names {
		p := cfg.Profiles[name]
		fmt.Printf("  %s - %s\n", name, p.Description)
		for _, svc := range p.Services {
			fmt.Printf("    - %s\n", svc)
		}
		fmt.Println()
	}
	return 0
}

// runProfilesAdd implements `flowlog profiles add <name> [-s svc1,svc2] [-d
// "description"] [--force]`. With -s, services are resolved by name or alias
// via resolveServiceToken (shared with the `start` picker); without it, the
// same interactive picker `start` uses is run instead.
func runProfilesAdd(args []string) int {
	usage := func() {
		fmt.Fprintln(os.Stderr, "Usage: flowlog profiles add <name> [-s svc1,svc2] [-d \"description\"] [--force]")
	}
	// <name> comes first, so it must be pulled out before fs.Parse: the flag
	// package stops parsing flags at the first non-flag argument.
	if len(args) == 0 {
		usage()
		return 1
	}
	name, rest := args[0], args[1:]

	fs := flag.NewFlagSet("profiles add", flag.ExitOnError)
	var services string
	fs.StringVar(&services, "s", "", "Comma-separated service names or aliases")
	var description string
	fs.StringVar(&description, "d", "", "Profile description")
	force := fs.Bool("force", false, "Overwrite an existing profile with this name")
	fs.Usage = usage
	fs.Parse(rest)

	if fs.NArg() != 0 {
		usage()
		return 1
	}
	if err := validateProfileName(name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	configPath, err := defaultConfigPath()
	if err != nil {
		fatalf("cannot resolve home directory: %v", err)
	}
	cfg, err := loadRawConfig(configPath)
	if err != nil {
		fatalf("%v", err)
	}
	if _, exists := cfg.Profiles[name]; exists && !*force {
		fmt.Fprintf(os.Stderr, "Profile '%s' already exists. Use --force to overwrite.\n", name)
		return 1
	}

	names := make([]string, len(cfg.Services))
	aliases := make([]string, len(cfg.Services))
	descriptions := make([]string, len(cfg.Services))
	for i, s := range cfg.Services {
		names[i], aliases[i], descriptions[i] = s.Name, s.Alias, s.Description
	}

	var resolved []string
	if services != "" {
		for _, tok := range splitCSV(services) {
			idx, err := resolveServiceToken(tok, names, aliases)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			resolved = append(resolved, names[idx])
		}
	} else {
		picks, err := pickServicesInteractively(names, aliases, descriptions)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if len(picks) == 0 {
			fmt.Println("No services selected. Cancelled.")
			return 0
		}
		for _, idx := range picks {
			resolved = append(resolved, names[idx])
		}
	}

	if err := addProfile(configPath, name, description, resolved); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("Profile '%s' saved: %s\n", name, strings.Join(resolved, ", "))
	return 0
}

// runProfilesRemove implements `flowlog profiles remove <name>`.
func runProfilesRemove(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: flowlog profiles remove <name>")
		return 1
	}
	name := args[0]

	configPath, err := defaultConfigPath()
	if err != nil {
		fatalf("cannot resolve home directory: %v", err)
	}
	removed, err := removeProfile(configPath, name)
	if err != nil {
		fatalf("%v", err)
	}
	if !removed {
		fmt.Fprintf(os.Stderr, "Profile '%s' not found.\n", name)
		return 1
	}
	fmt.Printf("Profile '%s' removed.\n", name)
	return 0
}

// validateProfileName rejects only empty names and names containing
// whitespace; anything else is a valid profile name.
func validateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name must not be empty")
	}
	if strings.ContainsFunc(name, unicode.IsSpace) {
		return fmt.Errorf("profile name must not contain whitespace")
	}
	return nil
}

// addProfile adds or overwrites profile name in the config at path,
// preserving every other field in the file verbatim (see configMap).
func addProfile(path, name, description string, services []string) error {
	cfg, err := loadConfigMap(path)
	if err != nil {
		return err
	}
	profiles := map[string]json.RawMessage{}
	if raw, ok := cfg["profiles"]; ok {
		if err := json.Unmarshal(raw, &profiles); err != nil {
			return err
		}
	}
	entry, err := json.Marshal(struct {
		Description string   `json:"description"`
		Services    []string `json:"services"`
	}{Description: description, Services: services})
	if err != nil {
		return err
	}
	profiles[name] = entry
	profilesJSON, err := json.Marshal(profiles)
	if err != nil {
		return err
	}
	cfg["profiles"] = profilesJSON
	return saveConfigMap(path, cfg)
}

// removeProfile deletes profile name from the config at path, preserving
// every other field verbatim. It reports whether the profile existed.
func removeProfile(path, name string) (bool, error) {
	cfg, err := loadConfigMap(path)
	if err != nil {
		return false, err
	}
	profiles := map[string]json.RawMessage{}
	if raw, ok := cfg["profiles"]; ok {
		if err := json.Unmarshal(raw, &profiles); err != nil {
			return false, err
		}
	}
	if _, exists := profiles[name]; !exists {
		return false, nil
	}
	delete(profiles, name)
	profilesJSON, err := json.Marshal(profiles)
	if err != nil {
		return false, err
	}
	cfg["profiles"] = profilesJSON
	if err := saveConfigMap(path, cfg); err != nil {
		return false, err
	}
	return true, nil
}
