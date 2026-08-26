package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestGate_AutoGCSkipsWhileDeployInFlight(t *testing.T) {
	g := newDeployGate()
	g.enterDeploy()
	if g.tryEnterGC() {
		t.Fatal("auto-GC acquired the gate during a deploy — the exact race this exists to prevent")
	}
	if g.idle() {
		t.Error("gate reported idle with a deploy in flight")
	}
	g.leaveDeploy()
	if !g.tryEnterGC() {
		t.Fatal("auto-GC could not acquire a free gate")
	}
	g.leaveGC()
}

func TestGate_ConcurrentDeploysDoNotBlockEachOther(t *testing.T) {
	g := newDeployGate()
	g.enterDeploy()

	done := make(chan struct{})
	go func() { g.enterDeploy(); close(done) }()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second deploy blocked behind the first; deploys are readers and must run concurrently")
	}
	g.leaveDeploy()
	g.leaveDeploy()
}

func TestGate_DeployWaitsForRunningGC(t *testing.T) {
	g := newDeployGate()
	if !g.tryEnterGC() {
		t.Fatal("could not acquire free gate")
	}

	var entered atomic.Bool
	go func() { g.enterDeploy(); entered.Store(true) }()

	time.Sleep(20 * time.Millisecond)
	if entered.Load() {
		t.Fatal("deploy started while GC held the gate")
	}
	g.leaveGC()
	waitFor(t, time.Second, "deploy to start after GC finished", entered.Load)
	g.leaveDeploy()
}

func TestGate_WaitingGCBlocksNewDeploys(t *testing.T) {
	// Writer priority: without it a steady stream of deploys starves a manual
	// GC forever.
	g := newDeployGate()
	g.enterDeploy() // in-flight deploy the manual GC must wait for

	var gcHolds atomic.Bool
	go func() {
		if err := g.enterGC(context.Background()); err == nil {
			gcHolds.Store(true)
		}
	}()
	waitFor(t, time.Second, "manual GC to start waiting", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.waiting > 0
	})

	var second atomic.Bool
	go func() { g.enterDeploy(); second.Store(true) }()

	time.Sleep(20 * time.Millisecond)
	if second.Load() {
		t.Fatal("a deploy jumped ahead of a waiting GC; repeated deploys would starve it")
	}

	g.leaveDeploy() // the in-flight deploy finishes
	waitFor(t, time.Second, "GC to acquire the gate", gcHolds.Load)
	if second.Load() {
		t.Fatal("queued deploy ran during GC")
	}

	g.leaveGC()
	waitFor(t, time.Second, "queued deploy to proceed", second.Load)
	g.leaveDeploy()
}

func TestGate_ManualGCTimesOutOnStuckDeploy(t *testing.T) {
	// A hung `docker pull` must not wedge GC — and, through it, every
	// subsequent deploy — forever.
	g := newDeployGate()
	g.enterDeploy()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := g.enterGC(ctx); err == nil {
		t.Fatal("enterGC returned nil despite an in-flight deploy and an expired context")
	}

	// The gate must be left untouched, and deploys must flow again.
	g.mu.Lock()
	held := g.gc
	g.mu.Unlock()
	if held {
		t.Error("gate still marked held after a timed-out enterGC")
	}
	done := make(chan struct{})
	go func() { g.enterDeploy(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deploys still blocked after the waiting GC gave up")
	}
	g.leaveDeploy()
	g.leaveDeploy()
}

func TestGate_NoOverlapUnderLoad(t *testing.T) {
	// Property check: across many interleaved deploys and sweeps, a GC never
	// observes a deploy in flight and vice versa.
	g := newDeployGate()
	var deploys, gcs atomic.Int32
	var violations atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				g.enterDeploy()
				deploys.Add(1)
				if gcs.Load() != 0 {
					violations.Add(1)
				}
				deploys.Add(-1)
				g.leaveDeploy()
			}
		}()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if !g.tryEnterGC() {
					continue
				}
				gcs.Add(1)
				if deploys.Load() != 0 {
					violations.Add(1)
				}
				gcs.Add(-1)
				g.leaveGC()
			}
		}()
	}
	wg.Wait()
	if v := violations.Load(); v != 0 {
		t.Fatalf("%d overlaps between deploys and GC", v)
	}
}

// --- worker scheduling ---

func newGCTestServer(sweep func()) *Server {
	s := &Server{
		gate:     newDeployGate(),
		gcSignal: make(chan struct{}, 1),
		gcQuiet:  5 * time.Millisecond,
	}
	s.gcSweep = sweep
	return s
}

func TestGCWorker_CoalescesBackToBackDeploys(t *testing.T) {
	// A push to main deploys test then prod seconds apart. Both request a
	// sweep; only one should run, and only after both have landed.
	var sweeps atomic.Int32
	s := newGCTestServer(func() { sweeps.Add(1) })
	go s.gcWorker()

	s.gate.enterDeploy()
	s.requestAutoGC() // test deploy
	s.requestAutoGC() // prod deploy, still in flight
	s.requestAutoGC()

	time.Sleep(30 * time.Millisecond)
	if n := sweeps.Load(); n != 0 {
		t.Fatalf("swept %d times while a deploy was in flight", n)
	}

	s.gate.leaveDeploy()
	waitFor(t, time.Second, "sweep after the deploys finished", func() bool {
		return sweeps.Load() == 1
	})

	time.Sleep(40 * time.Millisecond)
	if n := sweeps.Load(); n != 1 {
		t.Fatalf("expected the three requests to coalesce into 1 sweep, got %d", n)
	}
}

func TestGCWorker_GivesUpAfterSustainedDeploys(t *testing.T) {
	// A host that never goes quiet must not leave the worker spinning
	// forever — it drops the request and waits for the next deploy.
	var sweeps atomic.Int32
	s := newGCTestServer(func() { sweeps.Add(1) })
	s.gate.enterDeploy()

	done := make(chan struct{})
	go func() { s.autoGCWhenQuiet(); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never gave up on a permanently busy host")
	}
	if n := sweeps.Load(); n != 0 {
		t.Fatalf("swept %d times despite the deploy never finishing", n)
	}
	s.gate.leaveDeploy()
}

func TestRequestAutoGC_NeverBlocks(t *testing.T) {
	s := newGCTestServer(func() {})
	for i := 0; i < 100; i++ {
		done := make(chan struct{})
		go func() { s.requestAutoGC(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("requestAutoGC blocked; a deploy must never wait on the GC queue")
		}
	}
}
