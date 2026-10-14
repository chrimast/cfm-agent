package cfprobe

import "testing"

func TestTerminalWebSocketURL(t *testing.T) {
	got, err := terminalWebSocketURL("https://example.workers.dev/update", "srv-1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://example.workers.dev/agent/term?id=srv-1&secret=secret"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDefaultConfigDisablesTerminal(t *testing.T) {
	cfg := defaultConfig()
	if cfg.TerminalEnabled {
		t.Fatal("terminal must default off")
	}
}
