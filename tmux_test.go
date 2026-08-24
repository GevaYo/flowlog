package main

import "testing"

func TestIsNoServerErr(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{name: "no server running", stderr: "no server running on /private/tmp/tmux-502/default\n", want: true},
		{name: "error connecting after reboot wipes /tmp", stderr: "error connecting to /private/tmp/tmux-502/default (No such file or directory)\n", want: true},
		{name: "unrelated error", stderr: "session not found: foo\n", want: false},
		{name: "empty", stderr: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNoServerErr(tt.stderr); got != tt.want {
				t.Fatalf("isNoServerErr(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}
