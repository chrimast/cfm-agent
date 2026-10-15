//go:build linux

package cfprobe

import "testing"

func TestLoginShellArgs(t *testing.T) {
	got := loginShellArgs("/bin/bash")
	if len(got) != 2 || got[0] != "/bin/bash" || got[1] != "-il" {
		t.Fatalf("bash args = %v, want [/bin/bash -il]", got)
	}
	got = loginShellArgs("/bin/sh")
	if len(got) != 2 || got[0] != "/bin/sh" || got[1] != "-i" {
		t.Fatalf("sh args = %v, want [/bin/sh -i]", got)
	}
}

func TestLoginShellEnvSetsPrompt(t *testing.T) {
	t.Setenv("HOME", "/opt/cfm")
	env := loginShellEnv()
	foundPS1 := false
	foundTerm := false
	for _, item := range env {
		if item == `PS1=\u@\h:\w\$ ` {
			foundPS1 = true
		}
		if item == "TERM=xterm-256color" {
			foundTerm = true
		}
	}
	if !foundPS1 {
		t.Fatal("PS1 with working directory was not set")
	}
	if !foundTerm {
		t.Fatal("TERM=xterm-256color was not set")
	}
}
