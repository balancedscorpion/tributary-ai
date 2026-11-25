package jobs

import (
	"container/list"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrAdmissionQueueFull = errors.New("query admission queue is full")
	ErrAdmissionTimeout   = errors.New("query admission timed out waiting for a slot")
)

// AdmissionConfig bounds managed-job concurrency. Managed jobs queue for a
// slot instead of hard-failing; the queue itself is bounded and waiters
// expire after ReservationTTL so abandoned turns cannot pin slots.
//
// Legacy Register() jobs are counted separately under the legacy
// RegistryOptions limits and are unaffected by this config.
type AdmissionConfig struct {
	MaxGlobal          int           // default 8
	MaxPerThread       int           // default 3
	MaxPerConnection   int           // default 2
	MaxQueuedGlobal    int           // default 16
	MaxQueuedPerThread int           // default 8
	ReservationTTL     time.Duration // default 15m; bounds queue wait
	// CrossProcessRetryInterval paces re-attempts against a
	// CapacityReserver while queued: releases in other processes emit no
	// local wakeup, so waiters retry on this cadence. Default 2s.
	CrossProcessRetryInterval time.Duration

	// Nested profiling limits (see CapacityLimits): jobs in the profiling
	// phase count toward the overall limits AND these, so dry-run/EXPLAIN
	// work can never occupy every execution slot.
	MaxProfilingGlobal        int // default 4
	MaxProfilingPerThread     int // default 1
	MaxProfilingPerConnection int // default 1
}

func (c AdmissionConfig) withDefaults() AdmissionConfig {
	if c.MaxGlobal <= 0 {
		c.MaxGlobal = 8
	}
	if c.MaxPerThread <= 0 {
		c.MaxPerThread = 3
	}
	if c.MaxPerConnection <= 0 {
		c.MaxPerConnection = 2
	}
	if c.MaxQueuedGlobal <= 0 {
		c.MaxQueuedGlobal = 16
	}
	if c.MaxQueuedPerThread <= 0 {
		c.MaxQueuedPerThread = 8
	}
	if c.ReservationTTL <= 0 {
		c.ReservationTTL = 15 * time.Minute
	}
	if c.CrossProcessRetryInterval <= 0 {
		c.CrossProcessRetryInterval = 2 * time.Second
	}
	if c.MaxProfilingGlobal <= 0 {
		c.MaxProfilingGlobal = 4
	}
	if c.MaxProfilingPerThread <= 0 {
		c.MaxProfilingPerThread = 1
	}
	if c.MaxProfilingPerConnection <= 0 {
		c.MaxProfilingPerConnection = 1
	}
	return c
}

func (c AdmissionConfig) capacityLimits() CapacityLimits {
	return CapacityLimits{
		MaxGlobal:                 c.MaxGlobal,
		MaxPerThread:              c.MaxPerThread,
		MaxPerConnection:          c.MaxPerConnection,
		MaxProfilingGlobal:        c.MaxProfilingGlobal,
		MaxProfilingPerThread:     c.MaxProfilingPerThread,
		MaxProfilingPerConnection: c.MaxProfilingPerConnection,
	}
}

// connectionKey is the admission key for per-connection limits. Jobs with
// no explicit connection fall back to their provider so the limit still
// binds per warehouse.
func connectionKey(provider, connectionID string) string {
	connectionID = strings.TrimSpace(connectionID)
	if connectionID != "" {
		return strings.ToLower(connectionID)
	}
	return strings.TrimSpace(strings.ToLower(provider))
}

type admissionWaiter struct {
	threadID  string
	connKey   string
	profiling bool          // the waiter's job will enter the profiling phase
	ready     chan struct{} // closed exactly once when this waiter is chosen
}

type admissionState struct {
	mu      sync.Mutex
	cfg     AdmissionConfig
	waiters *list.List // of *admissionWaiter, FIFO among eligible
}

func newAdmissionState(cfg AdmissionConfig) *admissionState {
	return &admissionState{cfg: cfg.withDefaults(), waiters: list.New()}
}

func (a *admissionState) queuedCounts(threadID string) (global, thread int) {
	for e := a.waiters.Front(); e != nil; e = e.Next() {
		w := e.Value.(*admissionWaiter)
		global++
		if w.threadID == threadID {
			thread++
		}
	}
	return global, thread
}

// enqueue registers a waiter or reports queue-full. front re-inserts a
// woken waiter that lost the capacity race without losing its queue
// position.
func (a *admissionState) enqueue(threadID, connKey string, profiling, front bool) (*admissionWaiter, *list.Element, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	qGlobal, qThread := a.queuedCounts(threadID)
	if qGlobal >= a.cfg.MaxQueuedGlobal || qThread >= a.cfg.MaxQueuedPerThread {
		return nil, nil, ErrAdmissionQueueFull
	}
	w := &admissionWaiter{threadID: threadID, connKey: connKey, profiling: profiling, ready: make(chan struct{})}
	var el *list.Element
	if front {
		el = a.waiters.PushFront(w)
	} else {
		el = a.waiters.PushBack(w)
	}
	return w, el, nil
}

func (a *admissionState) remove(el *list.Element) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if el != nil {
		a.waiters.Remove(el)
	}
}

// releaseAll wakes every waiter (used by the reaper and on shutdown so
// nothing waits forever against a drained registry).
func (a *admissionState) releaseAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for el := a.waiters.Front(); el != nil; el = el.Next() {
		close(el.Value.(*admissionWaiter).ready)
	}
	a.waiters.Init()
}

// wakeEligibleWaiters wakes, in FIFO order, every queued waiter that fits
// the CURRENT counts (simulating the slots each woken waiter will take).
// Blind head-of-line wakeups deadlock: a waiter blocked on its thread or
// connection cap would be woken, fail, requeue, and nothing would ever
// signal the eligible waiter behind it. Called whenever capacity frees.
func (r *Registry) wakeEligibleWaiters() {
	type counts struct{ global, thread, conn int }

	r.mu.RLock()
	a := r.admission
	a.mu.Lock()
	if a.waiters.Len() == 0 {
		a.mu.Unlock()
		r.mu.RUnlock()
		return
	}
	cfg := a.cfg
	// Snapshot current usage once, then simulate admissions as we choose
	// waiters so we never over-wake. Profiling usage is tracked separately
	// for the nested limits.
	globalUsed := 0
	threadUsed := map[string]int{}
	connUsed := map[string]int{}
	profGlobalUsed := 0
	profThreadUsed := map[string]int{}
	profConnUsed := map[string]int{}
	for _, job := range r.jobs {
		if job == nil {
			continue
		}
		job.mu.RLock()
		if job.managed && isActiveStatus(job.Status) {
			threadID := strings.TrimSpace(job.Owner.ThreadID)
			connKey := connectionKey(job.Provider, job.ConnectionID)
			globalUsed++
			threadUsed[threadID]++
			connUsed[connKey]++
			if job.Status == JobStatusProfiling {
				profGlobalUsed++
				profThreadUsed[threadID]++
				profConnUsed[connKey]++
			}
		}
		job.mu.RUnlock()
	}

	var chosen []*admissionWaiter
	for el := a.waiters.Front(); el != nil; {
		next := el.Next()
		w := el.Value.(*admissionWaiter)
		fits := globalUsed < cfg.MaxGlobal &&
			threadUsed[w.threadID] < cfg.MaxPerThread &&
			connUsed[w.connKey] < cfg.MaxPerConnection
		if fits && w.profiling {
			fits = profGlobalUsed < cfg.MaxProfilingGlobal &&
				profThreadUsed[w.threadID] < cfg.MaxProfilingPerThread &&
				profConnUsed[w.connKey] < cfg.MaxProfilingPerConnection
		}
		if fits {
			globalUsed++
			threadUsed[w.threadID]++
			connUsed[w.connKey]++
			if w.profiling {
				profGlobalUsed++
				profThreadUsed[w.threadID]++
				profConnUsed[w.connKey]++
			}
			a.waiters.Remove(el)
			chosen = append(chosen, w)
		}
		el = next
	}
	a.mu.Unlock()
	r.mu.RUnlock()

	for _, w := range chosen {
		close(w.ready)
	}
}

type managedCounts struct {
	global, thread, conn             int
	profGlobal, profThread, profConn int
}

// managedActiveCountsLocked counts active managed jobs (overall and in the
// profiling phase). Caller must hold Registry.mu.
func (r *Registry) managedActiveCountsLocked(threadID, connKey string) managedCounts {
	threadID = strings.TrimSpace(threadID)
	var c managedCounts
	for _, job := range r.jobs {
		if job == nil {
			continue
		}
		job.mu.RLock()
		managed := job.managed
		status := job.Status
		jobThread := strings.TrimSpace(job.Owner.ThreadID)
		jobConn := connectionKey(job.Provider, job.ConnectionID)
		job.mu.RUnlock()
		if !managed || !isActiveStatus(status) {
			continue
		}
		sameThread := jobThread == threadID
		sameConn := connKey != "" && jobConn == connKey
		c.global++
		if sameThread {
			c.thread++
		}
		if sameConn {
			c.conn++
		}
		if status == JobStatusProfiling {
			c.profGlobal++
			if sameThread {
				c.profThread++
			}
			if sameConn {
				c.profConn++
			}
		}
	}
	return c
}

// hasManagedCapacityLocked reports whether a new managed job for
// (threadID, connKey) fits within admission limits — including the nested
// profiling limits when the job will enter the profiling phase. Caller
// holds Registry.mu.
func (r *Registry) hasManagedCapacityLocked(threadID, connKey string, profiling bool) bool {
	cfg := r.admission.cfg
	c := r.managedActiveCountsLocked(threadID, connKey)
	if c.global >= cfg.MaxGlobal || c.thread >= cfg.MaxPerThread || c.conn >= cfg.MaxPerConnection {
		return false
	}
	if profiling {
		if c.profGlobal >= cfg.MaxProfilingGlobal || c.profThread >= cfg.MaxProfilingPerThread || c.profConn >= cfg.MaxProfilingPerConnection {
			return false
		}
	}
	return true
}
