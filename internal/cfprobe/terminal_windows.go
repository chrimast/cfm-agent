//go:build windows

package cfprobe

func (a *Agent) startTerminalWorker(ctxDone <-chan struct{}) {
	a.log.info("terminal worker skipped on windows in this MVP")
}
