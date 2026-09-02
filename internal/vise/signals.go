package vise

import (
	"sync"
	"sync/atomic"
	"syscall"
)

// activeProbeGroup holds the process-group id of the probe currently
// running under this process, or 0 when no probe is running. Probes run in
// their own group so a timeout can kill every descendant; the same group id
// lets a signal delivered to vise stop the probe instead of orphaning it.
var activeProbeGroup atomic.Int64

// interrupted is set by the signal handler before it looks at the active
// group, so a probe started in the same instant kills itself on registration
// instead of outliving vise.
var interrupted atomic.Bool

func setActiveProbeGroup(pgid int) { activeProbeGroup.Store(int64(pgid)) }

// probeAboutToStart runs inside the lifecycle lock, between the interrupt
// check and cmd.Start. It is nil in production and exists so a test can open
// the exact window the lock closes: a signal arriving after a probe has been
// cleared to start and before its process group is registered. Without the
// lock the interrupt finds no group to kill and the probe outlives vise, which
// is the failure the lock is for and which no ordinary test can reach —
// removing the lock leaves every other test in this package green.
var probeAboutToStart func()

// probeLifecycle orders a signal against a probe start: runShell holds it
// from before cmd.Start until the group is registered, and the signal path
// holds it while it sets the flag and kills, so a signal either prevents the
// start or reaches a registered group — never the gap between.
var probeLifecycle sync.Mutex

// InterruptProbes records that vise is exiting on a signal and kills the
// running probe's process group, if any, under the lifecycle lock.
func InterruptProbes() {
	probeLifecycle.Lock()
	defer probeLifecycle.Unlock()
	interrupted.Store(true)
	KillActiveProbeGroup()
}

// KillActiveProbeGroup SIGKILLs the running probe's process group, if any.
// Callers invoke it from a signal handler before exiting so an interrupted
// vise never leaves its probe running and writing declared artifacts.
func KillActiveProbeGroup() {
	if pgid := activeProbeGroup.Load(); pgid != 0 {
		_ = syscall.Kill(-int(pgid), syscall.SIGKILL)
	}
}
