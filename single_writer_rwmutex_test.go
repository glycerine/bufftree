package bufftree

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func waitSingleWriter(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for lock transition")
		}
		runtime.Gosched()
	}
}

func awaitSingleWriter(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for lock operation")
	}
}

func panicSingleWriter(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("expected lock misuse to panic")
		}
	}()
	f()
}

func TestSingleWriterRWMutexZeroAndSize(t *testing.T) {
	var m SingleWriterRWMutex
	if n := unsafe.Sizeof(m); n != 4 {
		t.Fatalf("lock size = %d, want 4", n)
	}
	m.RLock()
	if s := m.state.Load(); s != 1 {
		t.Fatalf("read state = %x", s)
	}
	m.RUnlock()
	m.Lock()
	if s := m.state.Load(); s != singleWriterBit {
		t.Fatalf("write state = %x", s)
	}
	m.Unlock()
	m.Lock()
	m.Downgrade()
	if s := m.state.Load(); s != 1 {
		t.Fatalf("downgraded state = %x", s)
	}
	m.RUnlock()
	if s := m.state.Load(); s != 0 {
		t.Fatalf("final state = %x", s)
	}
	if n := testing.AllocsPerRun(100, func() {
		m.RLock()
		m.RUnlock()
		m.Lock()
		m.Unlock()
		m.Lock()
		m.Downgrade()
		m.RUnlock()
	}); n != 0 {
		t.Fatalf("allocations = %g, want 0", n)
	}
}

func TestSingleWriterRWMutexSharedReaders(t *testing.T) {
	var m SingleWriterRWMutex
	const readers = 8
	release := make(chan struct{})
	defer close(release)
	var entered atomic.Int32
	for i := 0; i < readers; i++ {
		go func() {
			m.RLock()
			entered.Add(1)
			<-release
			m.RUnlock()
		}()
	}
	waitSingleWriter(t, func() bool { return entered.Load() == readers })
	if s := m.state.Load(); s != readers {
		t.Fatalf("shared state = %x, want %d readers", s, readers)
	}
}

func TestSingleWriterRWMutexWriterReservation(t *testing.T) {
	var m SingleWriterRWMutex
	m.RLock()
	writerEntered := make(chan struct{})
	writerRelease := make(chan struct{})
	defer close(writerRelease)
	writerDone := make(chan struct{})
	go func() {
		m.Lock()
		close(writerEntered)
		<-writerRelease
		m.Unlock()
		close(writerDone)
	}()
	waitSingleWriter(t, func() bool { return m.state.Load() == singleWriterBit|1 })
	// Another writer waits rather than panicking or clearing the reservation.
	secondDone := make(chan struct{})
	go func() {
		m.Lock()
		m.Unlock()
		close(secondDone)
	}()
	readerStarted := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		close(readerStarted)
		m.RLock()
		m.RUnlock()
		close(readerDone)
	}()
	awaitSingleWriter(t, readerStarted)
	for i := 0; i < 100; i++ {
		runtime.Gosched()
		select {
		case <-writerEntered:
			t.Fatal("writer entered while original reader held the lock")
		case <-readerDone:
			t.Fatal("new reader bypassed reserved writer")
		case <-secondDone:
			t.Fatal("second writer bypassed reserved writer")
		default:
		}
	}
	m.RUnlock()
	awaitSingleWriter(t, writerEntered)
	if s := m.state.Load(); s != singleWriterBit {
		t.Fatalf("exclusive state = %x", s)
	}
	// Defer owns the channel close; use a second goroutine to check progress
	// after the release without closing it twice.
	t.Cleanup(func() {
		awaitSingleWriter(t, writerDone)
		awaitSingleWriter(t, readerDone)
		awaitSingleWriter(t, secondDone)
	})
}

func TestSingleWriterRWMutexDowngradeSharesAndPublishes(t *testing.T) {
	var m SingleWriterRWMutex
	var payload int
	start := make(chan struct{})
	read := make(chan struct{})
	var bad atomic.Bool
	// Start before the write, so the channel does not publish the payload.
	m.Lock()
	go func() {
		close(start)
		m.RLock()
		if payload != 42 {
			bad.Store(true)
		}
		m.RUnlock()
		close(read)
	}()
	awaitSingleWriter(t, start)
	payload = 42
	m.Downgrade()
	awaitSingleWriter(t, read) // Must finish while our downgraded hold remains.
	if bad.Load() {
		t.Error("reader did not observe exclusive write")
	}
	if s := m.state.Load(); s != 1 {
		t.Fatalf("downgrade lost its read hold: %x", s)
	}
	m.RUnlock()
}

func TestSingleWriterRWMutexDowngradeExcludesWriter(t *testing.T) {
	var m SingleWriterRWMutex
	var payload int
	m.Lock()
	payload = 42
	m.Downgrade()
	done := make(chan struct{})
	go func() {
		m.Lock()
		payload = 99
		m.Unlock()
		close(done)
	}()
	waitSingleWriter(t, func() bool { return m.state.Load() == singleWriterBit|1 })
	for i := 0; i < 100; i++ {
		runtime.Gosched()
		if payload != 42 {
			t.Fatal("writer changed data during downgraded read hold")
		}
	}
	m.RUnlock()
	awaitSingleWriter(t, done)
	m.RLock()
	if payload != 99 {
		t.Error("writer failed to publish after downgraded reader left")
	}
	m.RUnlock()
}

func TestSingleWriterRWMutexMisuse(t *testing.T) {
	for _, state := range []uint32{0, 1, singleWriterBit, singleWriterBit | 1} {
		for _, op := range []string{"Unlock", "RUnlock", "Downgrade"} {
			invalid := (op == "Unlock" || op == "Downgrade") && state != singleWriterBit ||
				op == "RUnlock" && state&singleReaderMask == 0
			if !invalid {
				continue
			}
			t.Run(fmt.Sprintf("%s/state_%x", op, state), func(t *testing.T) {
				var m SingleWriterRWMutex
				m.state.Store(state)
				ops := map[string]func(){"Unlock": m.Unlock, "RUnlock": m.RUnlock, "Downgrade": m.Downgrade}
				panicSingleWriter(t, ops[op])
				if s := m.state.Load(); s != state {
					t.Fatalf("misuse changed state from %x to %x", state, s)
				}
			})
		}
	}
	var m SingleWriterRWMutex
	m.state.Store(singleReaderMask)
	panicSingleWriter(t, m.RLock)
	if s := m.state.Load(); s != singleReaderMask {
		t.Fatalf("overflow corrupted state: %x", s)
	}
}

func TestSingleWriterRWMutexContendingWritersAndDowngrade(t *testing.T) {
	for _, procs := range []int{1, 4} {
		t.Run(fmt.Sprintf("P%d", procs), func(t *testing.T) {
			previous := runtime.GOMAXPROCS(procs)
			defer runtime.GOMAXPROCS(previous)
			var m SingleWriterRWMutex
			var a, b int
			var bad atomic.Bool
			var wg sync.WaitGroup
			const writers, iterations = 6, 1000
			m.Lock()
			for w := 0; w < writers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < iterations; i++ {
						m.Lock()
						a++
						if i%17 == 0 {
							runtime.Gosched()
						}
						b++
						if i%2 == 0 {
							want := a
							m.Downgrade()
							runtime.Gosched()
							if a != want || b != want {
								bad.Store(true)
							}
							m.RUnlock()
						} else {
							m.Unlock()
						}
					}
				}()
			}
			m.Downgrade()
			waitSingleWriter(t, func() bool { return m.state.Load() == singleWriterBit|1 })
			if a != 0 || b != 0 {
				t.Fatal("waiting writer entered across downgrade")
			}
			m.RUnlock()
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			awaitSingleWriter(t, done)
			if bad.Load() || a != writers*iterations || b != a {
				t.Fatalf("exclusive/downgraded access failed: a=%d b=%d", a, b)
			}
		})
	}
}

func TestSingleWriterRWMutexStress(t *testing.T) {
	for _, procs := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("P%d", procs), func(t *testing.T) {
			previous := runtime.GOMAXPROCS(procs)
			defer runtime.GOMAXPROCS(previous)
			var m SingleWriterRWMutex
			var a, b uint64 // Ordinary fields: race detector must see synchronization.
			var bad, stop atomic.Bool
			defer stop.Store(true)
			var reads atomic.Uint64
			var readers sync.WaitGroup
			for i := 0; i < 2*procs+1; i++ {
				readers.Add(1)
				go func() {
					defer readers.Done()
					for !stop.Load() {
						m.RLock()
						if a != b {
							bad.Store(true)
						}
						m.RUnlock()
						reads.Add(1)
						runtime.Gosched()
					}
				}()
			}
			waitSingleWriter(t, func() bool { return reads.Load() > 0 })
			done := make(chan struct{})
			go func() {
				for i := uint64(1); i <= 3000; i++ {
					m.Lock()
					a = i
					if i%31 == 0 {
						runtime.Gosched() // Exercise readers waiting on a descheduled writer.
					}
					b = i
					if i%2 == 0 {
						m.Downgrade()
						if a != b {
							bad.Store(true)
						}
						runtime.Gosched()
						m.RUnlock()
					} else {
						m.Unlock()
					}
				}
				stop.Store(true)
				readers.Wait()
				close(done)
			}()
			awaitSingleWriter(t, done)
			if bad.Load() {
				t.Fatal("reader observed inconsistent payload")
			}
			if s := m.state.Load(); s != 0 {
				t.Fatalf("final state = %x", s)
			}
		})
	}
}
