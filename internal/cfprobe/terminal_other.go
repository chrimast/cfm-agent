//go:build !linux && !windows

package cfprobe

func (a *Agent) startTerminalWorker(ctxDone <-chan struct{}) {
	a.log.info("terminal worker skipped on this platform in this MVP")
}
