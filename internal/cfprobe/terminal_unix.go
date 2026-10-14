//go:build !windows

package cfprobe

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
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
		}
	}()
}

func (a *Agent) runTerminalSession() error {
	cfg := a.configSnapshot()
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

func terminalWebSocketURL(workerURL, id, secret string) (string, error) {
	u, err := url.Parse(workerURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	u.Path = "/agent/term"
	q := u.Query()
	q.Set("id", id)
	q.Set("secret", secret)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func servePTY(conn *webSocketConn) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	ptmx, tty, err := openPTY()
	if err != nil {
		return err
	}
	cmd := exec.Command(shell)
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		return err
	}
	_ = tty.Close()
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				payload, _ := json.Marshal(termMessage{Type: "data", Data: string(buf[:n])})
				_ = conn.WriteText(payload, 5*time.Second)
			}
			if err != nil {
				close(done)
				return
			}
		}
	}()
	go func() {
		for {
			raw, _, err := conn.ReadDataMessage()
			if err != nil {
				_ = cmd.Process.Kill()
				return
			}
			var msg termMessage
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "input":
				_, _ = io.WriteString(ptmx, msg.Data)
			case "resize":
				_ = unix.IoctlSetWinsize(int(ptmx.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
					Row: uint16(max(msg.Rows, 1)),
					Col: uint16(max(msg.Cols, 1)),
				})
			case "open":
				_ = unix.IoctlSetWinsize(int(ptmx.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
					Row: uint16(max(msg.Rows, 24)),
					Col: uint16(max(msg.Cols, 80)),
				})
			}
		}
	}()
	err = cmd.Wait()
	_ = ptmx.Close()
	<-done
	exitPayload, _ := json.Marshal(termMessage{Type: "exit", Data: fmt.Sprintf("shell exit")})
	_ = conn.WriteText(exitPayload, 2*time.Second)
	return err
}

func openPTY() (*os.File, *os.File, error) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetInt(int(ptmx.Fd()), unix.TIOCSPTLCK, 0); err != nil {
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
