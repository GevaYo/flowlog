package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
)

func runStart(args []string) int {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	var profile string
	fs.StringVar(&profile, "p", "", "Use a named profile (skips interactive picker)")
	fs.StringVar(&profile, "profile", "", "Use a named profile (skips interactive picker)")
	skipAwsCheck := fs.Bool("skip-aws-check", false, "Skip AWS credential check")
	debug := fs.Bool("debug", false, "Show verbose output for AWS credential checks")
	// Hidden: lets this port run side by side with the TS tool during testing.
	session := fs.String("session", "dev-services", "")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: flowlog start [flags]")
		fmt.Fprintln(fs.Output(), "  -p, --profile <name>   Use a named profile (skips interactive picker)")
		fmt.Fprintln(fs.Output(), "  --skip-aws-check       Skip AWS credential check")
		fmt.Fprintln(fs.Output(), "  --debug                Show verbose output for AWS credential checks")
	}
	fs.Parse(args)

	if os.Getenv("TMUX") != "" {
		fmt.Fprintln(os.Stderr, "Error: This command cannot be run from inside a tmux session.")
		fmt.Fprintln(os.Stderr, "Please exit your current tmux session and try again.")
		return 1
	}

	if !*skipAwsCheck {
		// checkAwsCredentials prints its own error detail before returning.
		if err := checkAwsCredentials(*debug); err != nil {
			return 1
		}
	}

	configPath, err := defaultConfigPath()
	if err != nil {
		fatalf("cannot resolve home directory: %v", err)
	}
	cfg, err := loadRawConfig(configPath)
	if err != nil {
		fatalf("%v", err)
	}

	byName := make(map[string]int, len(cfg.Services))
	for i, s := range cfg.Services {
		byName[s.Name] = i
	}

	var selected []tmuxService

	if profile != "" {
		profileCfg, ok := cfg.Profiles[profile]
		if !ok {
			available := "none"
			if cfg.Profiles != nil {
				names := make([]string, 0, len(cfg.Profiles))
				for name := range cfg.Profiles {
					names = append(names, name)
				}
				available = strings.Join(names, ", ")
			}
			fmt.Fprintf(os.Stderr, "Profile '%s' not found. Available profiles: %s\n", profile, available)
			return 1
		}

		names := make([]string, 0, len(profileCfg.Services))
		for _, name := range profileCfg.Services {
			idx, ok := byName[name]
			if !ok {
				fmt.Fprintf(os.Stderr, "Profile '%s' references unknown service '%s'\n", profile, name)
				return 1
			}
			s := cfg.Services[idx]
			selected = append(selected, tmuxService{Name: s.Name, Alias: s.Alias, Command: s.Command, Env: s.Env})
			names = append(names, s.Name)
		}
		fmt.Printf("Using profile '%s': %s\n", profile, strings.Join(names, ", "))
	} else {
		names := make([]string, len(cfg.Services))
		aliases := make([]string, len(cfg.Services))
		descriptions := make([]string, len(cfg.Services))
		for i, s := range cfg.Services {
			names[i], aliases[i], descriptions[i] = s.Name, s.Alias, s.Description
		}
		picks, err := pickServicesInteractively(names, aliases, descriptions)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, idx := range picks {
			s := cfg.Services[idx]
			selected = append(selected, tmuxService{Name: s.Name, Alias: s.Alias, Command: s.Command, Env: s.Env})
		}
	}

	if len(selected) == 0 {
		fmt.Println("No services selected. Exiting...")
		return 0
	}

	sess := newTmuxSession(*session, cfg.ServicesRoot)
	if err := sess.createSession(); err != nil {
		fmt.Fprintf(os.Stderr, "Tmux Error: %v\n", err)
		return 1
	}
	for _, svc := range selected {
		if err := sess.createWindow(svc); err != nil {
			fmt.Fprintf(os.Stderr, "Tmux Error: %v\n", err)
			return 1
		}
	}

	if profile != "" {
		ready, err := sess.setupLogsWindow(profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Tmux Error: %v\n", err)
			return 1
		}
		if ready {
			fmt.Printf("Unified log view (flowlog) running in window 'logs' for profile '%s'.\n", profile)
		}
	}

	sess.printSessionInfo()
	return 0
}

// pickServicesInteractively prints a numbered list of services and reads a
// selection from stdin, shared by `start` (no -p) and `profiles add` (no -s).
// Empty input yields a nil pick list; callers decide whether that means
// cancel.
func pickServicesInteractively(names, aliases, descriptions []string) ([]int, error) {
	for i, name := range names {
		if descriptions[i] != "" {
			fmt.Printf("  %d) %s - %s\n", i+1, name, descriptions[i])
		} else {
			fmt.Printf("  %d) %s\n", i+1, name)
		}
	}
	fmt.Print("Select services (numbers, names, or aliases, space/comma separated; empty to cancel): ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return parseServiceSelection(line, names, aliases)
}

// parseServiceSelection splits picker input on commas/whitespace and resolves each
// token against a 1-based index, service name, or alias (case-insensitive),
// deduping while preserving first-mention order.
func parseServiceSelection(input string, names, aliases []string) ([]int, error) {
	tokens := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	var picks []int
	seen := make(map[int]bool, len(tokens))
	for _, tok := range tokens {
		idx, err := resolveServiceToken(tok, names, aliases)
		if err != nil {
			return nil, err
		}
		if !seen[idx] {
			seen[idx] = true
			picks = append(picks, idx)
		}
	}
	return picks, nil
}

func resolveServiceToken(tok string, names, aliases []string) (int, error) {
	if n, err := strconv.Atoi(tok); err == nil {
		if n >= 1 && n <= len(names) {
			return n - 1, nil
		}
		return 0, fmt.Errorf("unknown service '%s'", tok)
	}
	lower := strings.ToLower(tok)
	for i, name := range names {
		if strings.ToLower(name) == lower {
			return i, nil
		}
	}
	for i, alias := range aliases {
		if alias != "" && strings.ToLower(alias) == lower {
			return i, nil
		}
	}
	return 0, fmt.Errorf("unknown service '%s'", tok)
}
