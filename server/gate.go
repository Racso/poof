package server

import (
	"context"
	"sync"
)

// deployGate serializes image-destructive work (garbage collection) against
// image-creating work (deploy pulls).
//
// Docker's layer store is shared across every project on the host, so `docker
// rmi` and `docker image prune` can free layers that a concurrent `docker
// pull` is still extracting. That surfaces as a containerd overlayfs error
// ("failed to extract layer ... no such file or directory") and a 500 on an
// otherwise healthy deploy. Two projects deploying the same image back to back
// — the usual test-then-prod pattern — hit it often enough to matter.
//
// Deploys are readers: they race only with deletion, not with each other, so
// any number may run concurrently. GC is the writer and runs exclusively. A
// waiting writer blocks new deploys, so a steady stream of deploys cannot
// starve it.
type deployGate struct {
	mu      sync.Mutex
	cond    *sync.Cond
	deploys int  // deploys currently in flight
	gc      bool // a GC run holds the gate exclusively
	waiting int  // GC runs blocked waiting to acquire it
}

func newDeployGate() *deployGate {
	g := &deployGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// enterDeploy blocks until no GC is running or queued, then registers a deploy
// as in flight. Every call must be paired with leaveDeploy.
func (g *deployGate) enterDeploy() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.gc || g.waiting > 0 {
		g.cond.Wait()
	}
	g.deploys++
}

// leaveDeploy marks an in-flight deploy as finished.
func (g *deployGate) leaveDeploy() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.deploys > 0 {
		g.deploys--
	}
	g.cond.Broadcast()
}

// tryEnterGC acquires the gate for a GC run only if it is free right now. Used
// by automatic GC, which is opportunistic: if a deploy is in flight it is
// better to skip the sweep than to make the deploy wait for it.
func (g *deployGate) tryEnterGC() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gc || g.deploys > 0 {
		return false
	}
	g.gc = true
	return true
}

// enterGC blocks until the gate is free, then acquires it exclusively. Used by
// manual GC, which the operator asked for and which should therefore wait for
// in-flight deploys rather than skip. From the moment it starts waiting, new
// deploys queue behind it. Returns ctx.Err() if the context expires first, in
// which case the gate is not held.
func (g *deployGate) enterGC(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.waiting++
	defer func() {
		g.waiting--
		g.cond.Broadcast()
	}()

	// sync.Cond has no deadline, so a watchdog broadcasts on cancellation to
	// re-evaluate the loop condition.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			g.mu.Lock()
			g.cond.Broadcast()
			g.mu.Unlock()
		case <-stop:
		}
	}()

	for g.gc || g.deploys > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.cond.Wait()
	}
	g.gc = true
	return nil
}

// leaveGC releases the gate after a GC run.
func (g *deployGate) leaveGC() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gc = false
	g.cond.Broadcast()
}

// idle reports whether nothing is currently deploying or collecting.
func (g *deployGate) idle() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.deploys == 0 && !g.gc
}
