//go:build linux

package cfprobe

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type termMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func (a *Agent) startTerminalWorker(ctxDone <-chan struct{}) {
	go func() {
		backoff := time.Second
		for {
			select {
			case <-ctxDone:
				return
			default:
			}
			if !a.configSnapshot().TerminalEnabled {
				select {
				case <-ctxDone:
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			if err := a.runTerminalSession(); err != nil {
				a.log.debugf("terminal session: %v", err)
			}
			select {
			case <-ctxDone:
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			if a.configSnapshot().TerminalEnabled {
				backoff = time.Second
			}
		}
	}()
}

func (a *Agent) runTerminalSession() error {
	cfg := a.configSnapshot()
	if !cfg.TerminalEnabled {
		return nil
	}
	wsURL, err := terminalWebSocketURL(cfg.WorkerURL, cfg.ServerID, cfg.Secret)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, wsURL, nil)
	if err != nil {
		return err
	}
	a.setAgentHeaders(req)
	conn, _, _, _, err := dialReportWebSocket(a.ctx, wsURL, req.Header, true, 12*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	return servePTY(conn)
}

func servePTY(conn *webSocketConn) error {
	var mu sync.Mutex
	var ptmx *os.File
	var cmd *exec.Cmd
	var readerDone chan struct{}

	waitReader := func() {
		if readerDone != nil {
			<-readerDone
			readerDone = nil
		}
	}

	stopShell := func() {
		mu.Lock()
		current := cmd
		master := ptmx
		cmd = nil
		ptmx = nil
		mu.Unlock()
		if current != nil && current.Process != nil {
			_ = current.Process.Kill()
			_ = current.Wait()
		}
		if master != nil {
			_ = master.Close()
		}
		waitReader()
	}
	defer stopShell()

	startShell := func(cols, rows int) error {
		mu.Lock()
		alive := cmd != nil && cmd.Process != nil && cmd.ProcessState == nil
		master := ptmx
		mu.Unlock()
		if alive && master != nil {
			_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
				Row: uint16(max(rows, 1)),
				Col: uint16(max(cols, 1)),
			})
			return nil
		}
		stopShell()

		shell := resolveLoginShell()
		master, tty, err := openPTY()
		if err != nil {
			return err
		}
		command := exec.Command(shell)
		command.Args = loginShellArgs(shell)
		command.Env = loginShellEnv()
		command.Dir = loginHome()
		command.Stdin = tty
		command.Stdout = tty
		command.Stderr = tty
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
		if err := command.Start(); err != nil {
			_ = master.Close()
			_ = tty.Close()
			return err
		}
		_ = tty.Close()
		_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
			Row: uint16(max(rows, 24)),
			Col: uint16(max(cols, 80)),
		})
		done := make(chan struct{})
		mu.Lock()
		ptmx = master
		cmd = command
		readerDone = done
		mu.Unlock()
		go func() {
			defer close(done)
			buf := make([]byte, 4096)
			for {
				n, err := master.Read(buf)
				if n > 0 {
					payload, _ := json.Marshal(termMessage{Type: "data", Data: string(buf[:n])})
					_ = conn.WriteText(payload, 5*time.Second)
				}
				if err != nil {
					payload, _ := json.Marshal(termMessage{Type: "exit", Data: "shell exit"})
					_ = conn.WriteText(payload, 2*time.Second)
					mu.Lock()
					if ptmx == master {
						ptmx = nil
					}
					if cmd == command {
						cmd = nil
					}
					mu.Unlock()
					_ = command.Wait()
					_ = master.Close()
					return
				}
			}
		}()
		ready, _ := json.Marshal(termMessage{Type: "ready"})
		_ = conn.WriteText(ready, 2*time.Second)
		return nil
	}

	for {
		raw, _, err := conn.ReadDataMessage()
		if err != nil {
			stopShell()
			return err
		}
		var msg termMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "open":
			if err := startShell(msg.Cols, msg.Rows); err != nil {
				payload, _ := json.Marshal(termMessage{Type: "error", Data: err.Error()})
				_ = conn.WriteText(payload, 2*time.Second)
			}
		case "input":
			mu.Lock()
			master := ptmx
			mu.Unlock()
			if master != nil {
				_, _ = io.WriteString(master, msg.Data)
			}
		case "resize":
			mu.Lock()
			master := ptmx
			mu.Unlock()
			if master != nil {
				_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
					Row: uint16(max(msg.Rows, 1)),
					Col: uint16(max(msg.Cols, 1)),
				})
			}
		case "hello", "ping":
		}
	}
}

func loginHome() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	return "/opt/cfm"
}

func resolveLoginShell() string {
	for _, candidate := range []string{"/bin/bash", "/usr/bin/bash", os.Getenv("SHELL"), "/bin/ash", "/bin/sh"} {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "/bin/sh"
}

func loginShellArgs(shell string) []string {
	base := filepath.Base(shell)
	if base == "bash" {
		return []string{shell, "-il"}
	}
	return []string{shell, "-i"}
}

func loginShellEnv() []string {
	env := append([]string{}, os.Environ()...)
	home := loginHome()
	setEnv := func(key, value string) {
		prefix := key + "="
		for i, item := range env {
			if strings.HasPrefix(item, prefix) {
				env[i] = prefix + value
				return
			}
		}
		env = append(env, prefix+value)
	}
	setEnv("HOME", home)
	setEnv("TERM", "xterm-256color")
	setEnv("PS1", `\u@\h:\w\$ `)
	return env
}

func openPTY() (*os.File, *os.File, error) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	// TIOCSPTLCK takes a pointer to int; IoctlSetInt would pass 0 as an address and return EFAULT ("bad address").
	if err := unix.IoctlSetPointerInt(int(ptmx.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = ptmx.Close()
		return nil, nil, err
	}
	n, err := unix.IoctlGetInt(int(ptmx.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = ptmx.Close()
		return nil, nil, err
	}
	tty, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = ptmx.Close()
		return nil, nil, err
	}
	return ptmx, tty, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
