package bufftree

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
)

func mustTx(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func loadTx(t *testing.T, db *Tree[int, int], n int) {
	t.Helper()
	mustTx(t, db.Update(func(tx *WriteTx[int, int]) error {
		for i := 0; i < n; i++ {
			if err := tx.Put(i, i*10); err != nil {
				return err
			}
		}
		return nil
	}))
}
func assertBindings(t *testing.T, db *Tree[int, int], want map[int]int) {
	t.Helper()
	mustTx(t, db.View(func(tx *ReadOnlyTx[int, int]) error {
		got := map[int]int{}
		last := -1
		for k, v := range tx.All() {
			if k <= last {
				t.Fatalf("unordered %d after %d", k, last)
			}
			last = k
			got[k] = v
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("contents: got %v want %v", got, want)
		}
		if tx.Len() != int64(len(want)) {
			t.Fatalf("length %d want %d", tx.Len(), len(want))
		}
		it := tx.NewIter()
		it.SeekLast()
		keys := make([]int, 0, len(got))
		for it.Valid() {
			keys = append(keys, it.Key())
			it.Prev()
		}
		if len(keys) != len(got) || !slices.IsSortedFunc(keys, func(a, b int) int { return b - a }) {
			t.Fatalf("reverse: %v", keys)
		}
		return nil
	}))
}
func TestTxRandomRollbackAndPublication(t *testing.T) {
	for _, cfg := range []Config{tinyConfig, {}, {Fanout: 3, LogSize: 70, NumBlocks: 3, BlockSize: 4}} {
		db := NewBPTree[int, int](&cfg)
		model := map[int]int{}
		rng := rand.New(rand.NewSource(7))
		stop := errors.New("rollback")
		for batch := 0; batch < 180; batch++ {
			next := make(map[int]int, len(model))
			for k, v := range model {
				next[k] = v
			}
			rollback := batch%3 == 0
			err := db.Update(func(tx *WriteTx[int, int]) error {
				for step := 0; step < 30; step++ {
					k := rng.Intn(250)
					v := rng.Intn(10000)
					switch rng.Intn(12) {
					case 0, 1:
						mustTx(t, tx.Delete(k))
						delete(next, k)
					case 2:
						if batch%11 == 0 {
							_, err := tx.Clear()
							mustTx(t, err)
							clear(next)
						}
					case 3:
						hi := k + rng.Intn(20)
						bi, ei := rng.Intn(2) == 0, rng.Intn(2) == 0
						var count int64
						for key := range next {
							if (key > k || bi && key == k) && (key < hi || ei && key == hi) {
								delete(next, key)
								count++
							}
						}
						n, empty, err := tx.DeleteRange(k, hi, bi, ei)
						mustTx(t, err)
						if n != count || empty != (len(next) == 0) {
							t.Fatalf("delete range %d/%d empty=%v", n, count, empty)
						}
					default:
						mustTx(t, tx.Put(k, v))
						next[k] = v
					}
					if step%7 == 0 { // Empties count pending; publication must still sort these leaves.
						if tx.Len() != int64(len(next)) {
							t.Fatal("writer count")
						}
						it := tx.NewIter()
						it.SeekLast()
						if len(next) > 0 && !it.Valid() {
							t.Fatal("writer reverse")
						}
						it.Close()
					}
					got, ok, err := tx.Get(k)
					mustTx(t, err)
					want, present := next[k]
					if ok != present || ok && got != want {
						t.Fatal("read own writes")
					}
				}
				if rollback {
					return stop
				}
				return nil
			})
			if rollback {
				if err != stop {
					t.Fatal(err)
				}
			} else {
				mustTx(t, err)
				model = next
			}
			assertBindings(t, db, model)
			// Exercise all existing structural invariant checks on the final core.
			checkTree(t, &db.core, model)
		}
	}
}

func TestTxDeletingIteratorsAndNestedPreparation(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		db := NewBPTree[int, int](&tinyConfig)
		loadTx(t, db, 600)
		mustTx(t, db.Update(func(tx *WriteTx[int, int]) error {
			it, other := tx.NewIter(), tx.NewIter()
			other.Seek(300)
			if reverse {
				it.SeekLast()
			} else {
				it.SeekFirst()
			}
			n := 0
			for it.Valid() {
				k := it.Key()
				want := n
				if reverse {
					want = 599 - n
				}
				if k != want {
					t.Fatalf("step %d key %d", n, k)
				}
				mustTx(t, tx.Delete(k))
				// Nested scan preparation and new writes can move another cursor's storage.
				tx.Scan(0, 3, func(k, v int) bool { mustTx(t, tx.Put(k, v)); return true })
				if reverse {
					it.Prev()
				} else {
					it.Next()
				}
				n++
			}
			if n != 600 || tx.Len() != 0 {
				t.Fatal(n, tx.Len())
			}
			if other.Key() != 300 || other.Value() != 3000 {
				t.Fatal("current observation moved")
			}
			other.Next()
			if other.Valid() {
				t.Fatal("stale cursor survived root collapse")
			}
			return nil
		}))
		assertBindings(t, db, map[int]int{})
	}
}

func TestTxIteratorLiveDirectionClearAndWrap(t *testing.T) {
	var db Tree[int, int]
	loadTx(t, &db, 10)
	tx, _ := db.BeginUpdate()
	defer tx.Rollback()
	it := tx.NewIter()
	it.Next()
	it.Prev()
	if it.Valid() {
		t.Fatal("auto-positioned")
	}
	it.Seek(5)
	it.Prev()
	if it.Key() != 4 {
		t.Fatal("direction")
	}
	it.Next()
	if it.Key() != 5 {
		t.Fatal("direction")
	}
	mustTx(t, tx.Delete(5))
	mustTx(t, tx.Put(6, 66))
	it.Next()
	if it.Key() != 6 || it.Value() != 66 {
		t.Fatal("reseek")
	}
	_, err := tx.Clear()
	mustTx(t, err)
	mustTx(t, tx.Put(8, 80))
	it.Next()
	if it.Key() != 8 {
		t.Fatal("clear/reinsert")
	}
	it.SeekFirst()
	it.epoch = 0
	tx.s.epoch = math.MaxUint64
	mustTx(t, tx.Put(9, 90))
	if !it.stale {
		t.Fatal("epoch wrap did not invalidate")
	}
	it.Next()
	if it.Key() != 9 {
		t.Fatal("wrap recovery")
	}
	it.Next()
	mustTx(t, tx.Put(10, 100))
	it.Next()
	if it.Valid() {
		t.Fatal("end must stay invalid")
	}
	it.SeekLast()
	if it.Key() != 10 {
		t.Fatal("seek last")
	}
}

func TestTxSearchAndRanges(t *testing.T) {
	var db Tree[int, int]
	mustTx(t, db.Update(func(tx *WriteTx[int, int]) error {
		for _, k := range []int{-5, 0, 5, 10} {
			mustTx(t, tx.Put(k, k*2))
		}
		return nil
	}))
	mustTx(t, db.View(func(tx *ReadOnlyTx[int, int]) error {
		for _, tc := range []struct {
			m         SearchModifier
			k, want   int
			ok, exact bool
		}{
			{Exact, 0, 0, true, true}, {Exact, 1, 0, false, false}, {GTE, 1, 5, true, false}, {GT, 5, 10, true, false},
			{LTE, 4, 0, true, false}, {LT, 5, 0, true, false}, {LTE, 5, 5, true, true}, {GT, 10, 0, false, false}, {LT, -5, 0, false, false},
		} {
			kv, exact, err, it := tx.FindIt(tc.m, tc.k)
			mustTx(t, err)
			if (kv != nil) != tc.ok || exact != tc.exact || it.Valid() != tc.ok {
				t.Fatalf("search %+v", tc)
			}
			if kv != nil {
				if kv.Key != tc.want {
					t.Fatal(kv.Key, tc)
				}
				saved := kv.KV
				it.Next()
				if kv.KV != saved {
					t.Fatal("holder aliases iterator")
				}
			}
			kv.Close()
			it.Close()
		}
		kv, _, err, it := tx.FindIt(SearchModifier(100), 0)
		if err != ErrSearchModifier || it != nil || kv != nil {
			t.Fatal("bad modifier")
		}
		for _, tc := range []struct {
			fn   func(func(int, int) bool)
			want []int
		}{
			{func(f func(int, int) bool) { tx.Ascend(0, f) }, []int{0, 5, 10}},
			{func(f func(int, int) bool) { tx.Descend(0, f) }, []int{0, -5}},
			{func(f func(int, int) bool) { tx.AscendRange(0, 10, f) }, []int{0, 5}},
			{func(f func(int, int) bool) { tx.DescendRange(10, 0, f) }, []int{10, 5}},
			{func(f func(int, int) bool) { tx.Scan(0, 2, f) }, []int{0, 5}},
			{func(f func(int, int) bool) { tx.Range(10, 0, f) }, nil},
		} {
			var got []int
			tc.fn(func(k, v int) bool { got = append(got, k); return true })
			if !slices.Equal(got, tc.want) {
				t.Fatal(got, tc.want)
			}
		}
		for i := 0; i < 1000; i++ {
			kv, err := tx.GetKV(0)
			mustTx(t, err)
			kv.Close()
			kv.Close()
			it := tx.NewIter()
			it.Close()
		}
		if len(tx.s.iters) != 0 || len(tx.s.results) != 0 {
			t.Fatal("resource accumulation")
		}
		return nil
	}))
}

func TestTxEquivalentKeysRollback(t *testing.T) {
	type floatKey float64
	db := NewBPTree[floatKey, []int](&tinyConfig)
	nanA, nanB := floatKey(math.Float64frombits(0x7ff8000000000001)), floatKey(math.Float64frombits(0x7ff8000000000002))
	minusZero := floatKey(math.Copysign(0, -1))
	mustTx(t, db.Update(func(tx *WriteTx[floatKey, []int]) error {
		mustTx(t, tx.Put(nanA, nil))
		mustTx(t, tx.Put(minusZero, []int{7}))
		for i := 1; i < 60; i++ {
			mustTx(t, tx.Put(floatKey(i), []int{i}))
		}
		return nil
	}))
	stop := errors.New("stop")
	err := db.Update(func(tx *WriteTx[floatKey, []int]) error {
		mustTx(t, tx.Delete(nanB))
		mustTx(t, tx.Put(nanB, []int{2}))
		mustTx(t, tx.Delete(0))
		mustTx(t, tx.Put(0, nil))
		_, err := tx.Clear()
		mustTx(t, err)
		mustTx(t, tx.Put(nanB, []int{3}))
		_, err = tx.Clear()
		mustTx(t, err)
		return stop
	})
	if err != stop {
		t.Fatal(err)
	}
	mustTx(t, db.View(func(tx *ReadOnlyTx[floatKey, []int]) error {
		v, ok, err := tx.Get(nanB)
		mustTx(t, err)
		if !ok || v != nil {
			t.Fatal("nil versus missing")
		}
		it := tx.NewIter()
		it.SeekLast()
		if math.Float64bits(float64(it.Key())) != math.Float64bits(float64(nanA)) {
			t.Fatal("NaN encoding not restored")
		}
		it.SeekFirst()
		if !math.Signbit(float64(it.Key())) || it.Value()[0] != 7 {
			t.Fatal("negative zero not restored")
		}
		if tx.Len() != 61 {
			t.Fatal(tx.Len())
		}
		return nil
	}))
	var strings Tree[string, int]
	mustTx(t, strings.Update(func(tx *WriteTx[string, int]) error { return tx.Put("", 1) }))
	mustTx(t, strings.View(func(tx *ReadOnlyTx[string, int]) error {
		it := tx.NewIter()
		it.Seek("")
		if !it.Valid() || it.Value() != 1 {
			t.Fatal("empty string")
		}
		return nil
	}))
}

func TestTxLifecycle(t *testing.T) {
	stop := errors.New("stop")
	for _, action := range []string{"commit", "rollback", "error", "panic", "commit-error", "commit-panic"} {
		t.Run(action, func(t *testing.T) {
			var db Tree[int, int]
			loadTx(t, &db, 1)
			var it *Iter[int, int]
			var kv *KVcloser[int, int]
			var saved *WriteTx[int, int]
			panicked := false
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				err := db.Update(func(tx *WriteTx[int, int]) error {
					saved = tx
					mustTx(t, tx.Put(0, 99))
					it = tx.NewIter()
					it.SeekFirst()
					kv, _, _ = tx.Find(Exact, 0)
					switch action {
					case "commit":
						mustTx(t, tx.Commit())
						mustTx(t, tx.Rollback())
					case "rollback":
						mustTx(t, tx.Rollback())
						mustTx(t, tx.Commit())
					case "error":
						return stop
					case "panic":
						panic(stop)
					case "commit-error":
						mustTx(t, tx.Commit())
						return stop
					case "commit-panic":
						mustTx(t, tx.Commit())
						panic(stop)
					}
					return nil
				})
				if (action == "error" || action == "commit-error") && err != stop {
					t.Fatal(err)
				}
			}()
			if panicked != (action == "panic" || action == "commit-panic") {
				t.Fatal("panic semantics")
			}
			if it.Valid() || it.KV() != nil || kv.s != nil || kv.Value != 0 {
				t.Fatal("resources survived")
			}
			if err := saved.Put(2, 2); err != ErrTxClosed {
				t.Fatal(err)
			}
			if _, _, err := saved.Get(0); err != ErrTxClosed {
				t.Fatal(err)
			}
			if _, _, err, iter := saved.FindIt(GTE, 0); err != ErrTxClosed || iter != nil {
				t.Fatal("closed find")
			}
			mustTx(t, saved.Commit())
			mustTx(t, saved.Rollback())
			it.Next()
			it.Prev()
			it.SeekFirst()
			it.Close()
			kv.Close()
			want := 0
			if action == "commit" || action == "commit-error" || action == "commit-panic" {
				want = 99
			}
			assertBindings(t, &db, map[int]int{0: want})
		})
	}
	var db Tree[int, int]
	tx := db.BeginView()
	it := tx.NewIter()
	tx.Close()
	tx.Close()
	if it.Valid() {
		t.Fatal("closed iterator")
	}
	for _, fn := range []func(){func() { tx.Len() }, func() { tx.NewIter() }, func() { tx.Scan(0, 0, func(int, int) bool { return false }) }, func() { tx.All() }} {
		func() {
			defer func() {
				if recover() != ErrTxClosed {
					t.Error("expected closed panic")
				}
			}()
			fn()
		}()
	}
	w, _ := db.BeginUpdate()
	mustTx(t, w.Put(1, 1))
	mustTx(t, w.Rollback())
	mustTx(t, w.Commit())
	assertBindings(t, &db, map[int]int{})
}

func TestTxCallbackTerminationAndMerge(t *testing.T) {
	var db Tree[int, int]
	loadTx(t, &db, 20)
	mustTx(t, db.Update(func(tx *WriteTx[int, int]) error {
		calls := 0
		tx.Range(0, 20, func(k, v int) bool { calls++; mustTx(t, tx.Put(30, 30)); mustTx(t, tx.Commit()); return true })
		if calls != 1 {
			t.Fatal("continued after commit")
		}
		return nil
	}))
	mustTx(t, db.View(func(tx *ReadOnlyTx[int, int]) error {
		calls := 0
		tx.MapRange(0, 40, func(k, v int) bool { calls++; tx.Close(); return true })
		if calls != 1 {
			t.Fatal("continued closed view")
		}
		return nil
	}))
	tx, _ := db.BeginUpdate()
	defer tx.Rollback()
	mustTx(t, tx.Merge(0, func(old int, ok bool) (int, bool, bool) {
		if !ok || old != 0 {
			t.Fatal("merge input")
		}
		mustTx(t, tx.Put(0, 1))
		return 2, true, false
	}))
	if v, _, _ := tx.Get(0); v != 2 {
		t.Fatal("merge action")
	}
	if err := tx.Merge(0, func(int, bool) (int, bool, bool) { return 3, true, true }); err != ErrMergeAction {
		t.Fatal(err)
	}
	mustTx(t, tx.Merge(0, func(int, bool) (int, bool, bool) { return 0, false, false }))
	mustTx(t, tx.Merge(1, func(int, bool) (int, bool, bool) { return 0, false, true }))
	if err := tx.Merge(0, func(int, bool) (int, bool, bool) { mustTx(t, tx.Rollback()); return 4, true, false }); err != ErrTxClosed {
		t.Fatal(err)
	}
	assertBindings(t, &db, func() map[int]int {
		m := map[int]int{30: 30}
		for i := 0; i < 20; i++ {
			m[i] = i * 10
		}
		return m
	}())
}

func waitTx(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("transaction did not finish")
	}
}
func TestTxLockOwnershipAndGoexit(t *testing.T) {
	var db Tree[int, int]
	for _, write := range []bool{false, true} {
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			if write {
				_ = db.Update(func(tx *WriteTx[int, int]) error { _ = tx.Commit(); close(entered); <-release; return nil })
			} else {
				_ = db.View(func(tx *ReadOnlyTx[int, int]) error { tx.Close(); close(entered); <-release; return nil })
			}
		}()
		waitTx(t, entered)
		if db.mu.TryLock() {
			db.mu.Unlock()
			t.Fatal("managed callback released ownership early")
		}
		close(release)
		waitTx(t, done)
	}
	// Two readers really overlap, and a pending writer proceeds after both end.
	a, b := db.BeginView(), db.BeginView()
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		_ = db.Update(func(tx *WriteTx[int, int]) error { return tx.Put(1, 1) })
		close(done)
	}()
	waitTx(t, started)
	if db.mu.TryLock() {
		db.mu.Unlock()
		t.Fatal("writer overlapped readers")
	}
	a.Close()
	b.Close()
	waitTx(t, done)
	// Goexit executes the deferred rollback even though recover would return nil.
	done = make(chan struct{})
	go func() {
		defer close(done)
		_ = db.Update(func(tx *WriteTx[int, int]) error { _ = tx.Put(1, 2); runtime.Goexit(); return nil })
	}()
	waitTx(t, done)
	assertBindings(t, &db, map[int]int{1: 1})
}

// Include all shared physical state, not just logical enumeration. fmt's
// representation includes sorted flags, header mirrors, dirty links and scratch.
func physicalTx(db *Tree[int, int]) string {
	c := &db.core
	out := fmt.Sprintf("%p %v %d %p %d %v %v %p", c.root, c.cfg, c.length, c.pending, c.version, c.mapBuffers, c.rebuildBuffer, c.preparing)
	var walk func(*node[int, int])
	walk = func(n *node[int, int]) {
		if n == nil {
			return
		}
		out += fmt.Sprintf("%p %v %d %p %p %p %v", n, n.keys, n.min, n.parent, n.prev, n.next, n.children)
		if n.leaf != nil {
			out += fmt.Sprintf("%+v", *n.leaf)
		}
		for _, ch := range n.children {
			walk(ch)
		}
	}
	walk(c.root)
	return out
}
func TestTxConcurrentReadersPhysicallyPure(t *testing.T) {
	db := NewBPTree[int, int](&tinyConfig)
	loadTx(t, db, 500)
	before := physicalTx(db)
	var wg sync.WaitGroup
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = db.View(func(tx *ReadOnlyTx[int, int]) error {
					count := 0
					tx.Scan(0, 1000, func(k, v int) bool {
						count++
						if v != k*10 {
							t.Error("value")
						}
						return true
					})
					if count != 500 || tx.Len() != 500 {
						t.Error("count")
					}
					it := tx.NewIter()
					for it.SeekLast(); it.Valid(); it.Prev() {
						_, _, _ = tx.Get(it.Key())
					}
					return nil
				})
			}
		}()
	}
	wg.Wait()
	if after := physicalTx(db); after != before {
		t.Fatal("shared readers changed tree storage")
	}
}
func TestTxWholeSpanIsolation(t *testing.T) {
	db := NewBPTree[int, int](&tinyConfig)
	loadTx(t, db, 100)
	mustTx(t, db.Update(func(tx *WriteTx[int, int]) error {
		for i := 0; i < 100; i++ {
			mustTx(t, tx.Put(i, 0))
		}
		return nil
	}))
	var wg sync.WaitGroup
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = db.View(func(tx *ReadOnlyTx[int, int]) error {
					want := -1
					n := 0
					for _, v := range tx.All() {
						if want < 0 {
							want = v
						}
						if v != want {
							t.Error("torn span")
						}
						n++
					}
					if n != 100 {
						t.Error("missing span")
					}
					return nil
				})
			}
		}()
	}
	for j := 1; j <= 100; j++ {
		err := db.Update(func(tx *WriteTx[int, int]) error {
			for i := 0; i < 100; i++ {
				mustTx(t, tx.Put(i, j))
			}
			if j%3 == 0 {
				return errors.New("undo")
			}
			return nil
		})
		if j%3 != 0 {
			mustTx(t, err)
		}
	}
	wg.Wait()
}
func TestTxStreamingEarlyStop(t *testing.T) {
	db := NewBPTree[int, int](&tinyConfig)
	loadTx(t, db, 10000)
	tx, _ := db.BeginUpdate()
	defer tx.Rollback()
	// Dirty a later leaf: visiting just the first entry must not prepare it.
	mustTx(t, tx.Put(9998, -1))
	mustTx(t, tx.Put(9997, -2))
	later := db.core.findLeaf(9998).leaf
	before := fmt.Sprintf("%+v", *later)
	n := 0
	tx.Scan(0, 10000, func(k, v int) bool { n++; return false })
	if n != 1 || fmt.Sprintf("%+v", *later) != before {
		t.Fatal("early stop prepared later storage")
	}
	if len(tx.s.iters) != 0 {
		t.Fatal("scan resource leak")
	}
}

func TestTxPendingWriterGatesNewReaders(t *testing.T) {
	var db Tree[int, int]
	existing := db.BeginView()
	started, writerEntered, releaseWriter, writerDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(writerDone)
		close(started)
		_ = db.Update(func(tx *WriteTx[int, int]) error { close(writerEntered); <-releaseWriter; return tx.Put(1, 1) })
	}()
	waitTx(t, started)
	// With an existing reader held, failed TryRLock identifies writer admission
	// closing to newcomers. No sleeps or assumptions about goroutine FIFO order.
	deadline := time.Now().Add(10 * time.Second)
	for db.mu.TryRLock() {
		db.mu.RUnlock()
		if time.Now().After(deadline) {
			existing.Close()
			close(releaseWriter)
			t.Fatal("writer did not announce itself")
		}
		runtime.Gosched()
	}
	lateDone := make(chan struct{})
	go func() {
		defer close(lateDone)
		_ = db.View(func(tx *ReadOnlyTx[int, int]) error {
			v, ok, _ := tx.Get(1)
			if !ok || v != 1 {
				t.Error("new reader bypassed pending writer")
			}
			return nil
		})
	}()
	existing.Close()
	waitTx(t, writerEntered)
	if db.mu.TryRLock() {
		db.mu.RUnlock()
		t.Error("reader admitted during writer")
	}
	close(releaseWriter)
	waitTx(t, writerDone)
	waitTx(t, lateDone)
}

func TestTxPureCursorBoundaries(t *testing.T) {
	rng := rand.New(rand.NewSource(41))
	db := NewBPTree[int, int](&tinyConfig)
	model := map[int]int{}
	for round := 0; round < 30; round++ {
		mustTx(t, db.Update(func(tx *WriteTx[int, int]) error {
			for i := 0; i < 100; i++ {
				k := rng.Intn(100)
				if rng.Intn(3) == 0 {
					mustTx(t, tx.Delete(k))
					delete(model, k)
				} else {
					v := rng.Intn(10000)
					mustTx(t, tx.Put(k, v))
					model[k] = v
				}
			}
			return nil
		}))
		mustTx(t, db.View(func(tx *ReadOnlyTx[int, int]) error {
			keys := make([]int, 0, len(model))
			for k := range model {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for target := -1; target <= 101; target++ {
				for _, mod := range []SearchModifier{Exact, GTE, GT, LTE, LT} {
					want := -1
					for _, k := range keys {
						switch mod {
						case Exact:
							if k == target {
								want = k
							}
						case GTE:
							if k >= target && want < 0 {
								want = k
							}
						case GT:
							if k > target && want < 0 {
								want = k
							}
						case LTE:
							if k <= target {
								want = k
							}
						case LT:
							if k < target {
								want = k
							}
						}
					}
					kv, exact, err := tx.Find(mod, target)
					mustTx(t, err)
					if (kv != nil) != (want >= 0) {
						t.Fatalf("mod %v target %d want %d got %v", mod, target, want, kv)
					}
					if kv != nil && (kv.Key != want || kv.Value != model[want] || exact != (want == target)) {
						t.Fatal("search pair")
					}
					kv.Close()
				}
			}
			it := tx.NewIter()
			i := len(keys) - 1
			for it.SeekLast(); it.Valid(); it.Prev() {
				if i < 0 || it.Key() != keys[i] || it.Value() != model[keys[i]] {
					t.Fatal("reverse merge pair")
				}
				i--
			}
			if i != -1 {
				t.Fatal("reverse missing")
			}
			return nil
		}))
	}
}

func TestTxCapabilitiesAndHolderOwnership(t *testing.T) {
	var db Tree[int, int]
	loadTx(t, &db, 2)
	ro := db.BeginView()
	if _, ok := any(ro).(interface{ Put(int, int) error }); ok {
		t.Fatal("read-only handle offers mutation")
	}
	if _, ok := any(&db).(interface{ Get(int) (int, bool, error) }); ok {
		t.Fatal("database bypasses transactions")
	}
	kv, err := ro.GetKV(0)
	mustTx(t, err)
	kv.Close()
	if db.mu.TryLock() {
		db.mu.Unlock()
		t.Fatal("holder closed the transaction")
	}
	ro.Close()
	w, _ := db.BeginUpdate()
	if _, ok := any(w).(interface{ Close() }); ok {
		t.Fatal("write handle exposes read unlock")
	}
	_ = w.Rollback()
}

func TestTxManagedViewPanicAndGoexit(t *testing.T) {
	var db Tree[int, int]
	func() {
		defer func() {
			if recover() != "view panic" {
				t.Error("lost panic")
			}
		}()
		_ = db.View(func(tx *ReadOnlyTx[int, int]) error { tx.NewIter(); panic("view panic") })
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = db.View(func(tx *ReadOnlyTx[int, int]) error { runtime.Goexit(); return nil })
	}()
	waitTx(t, done)
	mustTx(t, db.Update(func(tx *WriteTx[int, int]) error { return tx.Put(0, 0) }))
}
