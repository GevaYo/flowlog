package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// tmuxSession drives one tmux session used to run services in their own windows.
type tmuxSession struct {
	name         string
	servicesRoot string
}

func newTmuxSession(name string, servicesRoot string) *tmuxSession {
	return &tmuxSession{name: name, servicesRoot: servicesRoot}
}

// tmuxService is what's needed to launch a service in its own tmux window.
// Broader than the package's Service type (types.go), which only carries what
// flowlog needs to tail an already-running service's log file.
type tmuxService struct {
	Name    string
	Alias   string
	Command string
	Env     map[string]string
}

// isNoServerErr reports whether tmux stderr indicates there's no tmux server
// running rather than a real error. Normally tmux says "no server running";
// after a reboot wipes /tmp, the socket file is also gone and tmux instead
// says "error connecting to ... (No such file or directory)". Both mean the
// same thing: the subsequent new-session -d will start a fresh server.
func isNoServerErr(stderr string) bool {
	return strings.Contains(stderr, "no server running") || strings.Contains(stderr, "error connecting")
}

// run executes a tmux subcommand and returns its stdout. A "no server running"
// stderr (e.g. from list-sessions before any session exists) is treated as
// empty output rather than an error.
func (t *tmuxSession) run(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && isNoServerErr(string(exitErr.Stderr)) {
			return "", nil
		}
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (t *tmuxSession) createSession() error {
	out, err := t.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		return err
	}
	if strings.Contains(out, t.name) {
		if _, err := t.run("kill-session", "-t", t.name); err != nil {
			return err
		}
	}
	if _, err := t.run("new-session", "-d", "-s", t.name); err != nil {
		return err
	}
	// Generous scrollback for log-heavy service windows (must be set before windows are created).
	if _, err := t.run("set-option", "-g", "history-limit", "100000"); err != nil {
		return err
	}
	// Window 0 was created by new-session before the option applied; recreate it so it gets the new limit.
	if _, err := t.run("new-window", "-k", "-t", t.name+":0"); err != nil {
		return err
	}
	return nil
}

func (t *tmuxSession) createWindow(svc tmuxService) error {
	name := svc.Alias
	if name == "" {
		name = svc.Name
	}
	dir := filepath.Join(t.servicesRoot, svc.Name)
	target := t.name + ":" + name

	if _, err := t.run("new-window", "-t", t.name, "-n", name); err != nil {
		return err
	}
	if _, err := t.run("send-keys", "-t", target, "cd "+dir, "C-m"); err != nil {
		return err
	}
	if len(svc.Env) > 0 {
		keys := make([]string, 0, len(svc.Env))
		for k := range svc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys) // map order is undefined in Go; sort for deterministic output
		exports := make([]string, 0, len(keys))
		for _, k := range keys {
			exports = append(exports, fmt.Sprintf(`export %s="%s"`, k, svc.Env[k]))
		}
		if _, err := t.run("send-keys", "-t", target, strings.Join(exports, " && "), "C-m"); err != nil {
			return err
		}
	}
	if _, err := t.run("send-keys", "-t", target, svc.Command, "C-m"); err != nil {
		return err
	}
	return nil
}

// setupLogsWindow renames window 0 to "logs" and runs this flowlog binary
// there so the session opens with a unified log view. Unlike the TS original,
// it does not probe PATH via a login shell; it re-execs its own binary path.
func (t *tmuxSession) setupLogsWindow(profileName string) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("\nTip: install flowlog to get a unified log view in the first tmux window.")
		return false, nil
	}
	target := t.name + ":0"
	if _, err := t.run("rename-window", "-t", target, "logs"); err != nil {
		return false, err
	}
	cmd := fmt.Sprintf("%s -f --profile %s --ship", exe, profileName)
	if _, err := t.run("send-keys", "-t", target, cmd, "C-m"); err != nil {
		return false, err
	}
	return true, nil
}

func (t *tmuxSession) printSessionInfo() {
	fmt.Println("\nTmux session created successfully!")
	fmt.Println("To attach to the session, run:")
	fmt.Println("flowlog attach")
	fmt.Println("or")
	fmt.Printf("tmux attach-session -t %s\n", t.name)
	fmt.Println("\nUseful tmux commands:")
	fmt.Println("- Ctrl+b n: Next window")
	fmt.Println("- Ctrl+b p: Previous window")
	fmt.Println("- Ctrl+b d: Detach from session")
	fmt.Println("- Ctrl+b c: Create new window")
}

func (t *tmuxSession) attachToSession() error {
	if err := exec.Command("tmux", "has-session", "-t", t.name).Run(); err != nil {
		return fmt.Errorf("No active tmux session named %q found. Start one with \"flowlog start\" first.", t.name)
	}
	attach := exec.Command("tmux", "attach-session", "-t", t.name)
	attach.Stdin = os.Stdin
	attach.Stdout = os.Stdout
	attach.Stderr = os.Stderr
	if err := attach.Run(); err != nil {
		return fmt.Errorf("tmux attach-session -t %s: %w", t.name, err)
	}
	return nil
}
