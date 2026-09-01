package vise

import (
	"sync/atomic"
	"syscall"
)

// activeProbeGroup holds the process-group id of the probe currently
// running under this process, or 0 when no probe is running. Probes run in
// their own group so a timeout can kill every descendant; the same group id
// lets a signal delivered to vise stop the probe instead of orphaning it.
var activeProbeGroup atomic.Int64

func setActiveProbeGroup(pgid int) { activeProbeGroup.Store(int64(pgid)) }

// KillActiveProbeGroup SIGKILLs the running probe's process group, if any.
// Callers invoke it from a signal handler before exiting so an interrupted
// vise never leaves its probe running and writing declared artifacts.
func KillActiveProbeGroup() {
	if pgid := activeProbeGroup.Load(); pgid != 0 {
		_ = syscall.Kill(-int(pgid), syscall.SIGKILL)
	}
}
