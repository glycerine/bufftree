package bufftree

import (
	"runtime"
	"sync/atomic"
)

const (
	singleWriterBit  uint32 = 1 << 31
	singleReaderMask        = singleWriterBit - 1
	singleWriterSpin        = 32
)

// SingleWriterRWMutex is a spin-first reader/writer lock with atomic downgrade.
// Its zero value is unlocked. It must not be copied after first use.
//
// Any goroutine may acquire it for reading or writing. Concurrent Lock calls
// wait: "single writer" means one admitted writer at a time, not one permitted
// caller. Like sync.RWMutex, it admits many readers or one exclusive writer.
//
// A reserved writer blocks new readers while existing readers finish. Waiting
// spins briefly, then yields to the Go scheduler; there is no lock-local waiter
// queue and no FIFO or bounded-wait guarantee. Long critical sections can waste
// CPU despite yielding. Recursive read locking and read-to-write upgrade are
// unsupported. Downgrade retains one read hold without an unlocked interval.
//
// This primitive does not provide transaction isolation.
type SingleWriterRWMutex struct {
	// The high bit reserves the single writer. Once the low bits (active
	// readers) reach zero, that writer has exclusive access.
	state atomic.Uint32
}

// Lock waits for any previous writer, reserves exclusive access, then waits
// for existing readers to finish. It must not be called while the caller
// already holds this lock for reading or writing.
func (m *SingleWriterRWMutex) Lock() {
	var wait singleWriterWait
	for {
		s := m.state.Load()
		if s&singleWriterBit == 0 && m.state.CompareAndSwap(s, s|singleWriterBit) {
			break
		}
		wait.pause()
	}
	for m.state.Load() != singleWriterBit {
		wait.pause()
	}
}

// Unlock releases exclusive access. It panics unless the lock is exclusive.
func (m *SingleWriterRWMutex) Unlock() {
	if !m.state.CompareAndSwap(singleWriterBit, 0) {
		panic("bufftree: SingleWriterRWMutex.Unlock without exclusive access")
	}
}

// Downgrade atomically replaces exclusive access with one read hold. The caller
// must subsequently call RUnlock, not Unlock. A later writer can reserve the
// lock immediately, but cannot acquire exclusive access until readers finish.
func (m *SingleWriterRWMutex) Downgrade() {
	if !m.state.CompareAndSwap(singleWriterBit, 1) {
		panic("bufftree: SingleWriterRWMutex.Downgrade without exclusive access")
	}
}

// RLock acquires shared access. Once a writer reserves the lock, new readers
// wait until it unlocks or downgrades. Read locks cannot be recursively acquired.
func (m *SingleWriterRWMutex) RLock() {
	var wait singleWriterWait
	for {
		s := m.state.Load()
		if s&singleWriterBit == 0 {
			if s == singleReaderMask {
				panic("bufftree: SingleWriterRWMutex reader count overflow")
			}
			if m.state.CompareAndSwap(s, s+1) {
				return
			}
		}
		// Poll with loads while the writer bit is set; don't repeatedly
		// issue failed read-modify-write operations against a busy writer.
		wait.pause()
	}
}

// RUnlock releases one read hold. It panics if there are no active readers.
func (m *SingleWriterRWMutex) RUnlock() {
	var wait singleWriterWait
	for {
		s := m.state.Load()
		if s&singleReaderMask == 0 {
			panic("bufftree: SingleWriterRWMutex.RUnlock without shared access")
		}
		// Subtraction preserves a pending writer's reservation. CAS also
		// makes an invalid unlock fail without corrupting the lock state.
		if m.state.CompareAndSwap(s, s-1) {
			return
		}
		wait.pause()
	}
}

type singleWriterWait struct{ spins uint8 }

func (w *singleWriterWait) pause() {
	if w.spins < singleWriterSpin {
		w.spins++
		return
	}
	// Yield on every subsequent failed attempt. In particular, a waiter
	// must not monopolize the only P when GOMAXPROCS=1.
	runtime.Gosched()
}
