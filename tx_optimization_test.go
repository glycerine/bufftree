package bufftree

import (
	"errors"
	"math"
	"math/rand"
	"testing"
)

func TestMembershipSurvivesStructuralChanges(t *testing.T) {
	for _, cfg := range []Config{tinyConfig, {}, {Fanout: 3, LogSize: 70, NumBlocks: 3, BlockSize: 4}} {
		db := NewBPTree[int, int](&cfg)
		rng := rand.New(rand.NewSource(93))
		for round := 0; round < 100; round++ {
			err := db.Update(func(tx *WriteTx[int, int]) error {
				for i := 0; i < 200; i++ {
					k := rng.Intn(2000)
					if rng.Intn(3) == 0 {
						mustTx(t, tx.Delete(k))
					} else {
						mustTx(t, tx.Put(k, i))
					}
				}
				// Includes buffered records, not just records already redistributed.
				for n := db.core.firstLeaf(); n != nil; n = n.next {
					p := n.leaf
					check := func(e entry[int, int]) {
						if !p.mayContain(membershipHash(e.key)) {
							t.Fatalf("false negative for key %d", e.key)
						}
					}
					for _, e := range p.log() {
						check(e)
					}
					for i := 0; i < p.headerN; i++ {
						check(*p.header(i))
						for _, e := range p.block(i) {
							check(e)
						}
					}
				}
				if round%3 == 0 {
					return errors.New("rollback")
				}
				return nil
			})
			if round%3 != 0 {
				mustTx(t, err)
			}
			mustTx(t, db.View(func(tx *ReadOnlyTx[int, int]) error {
				for k := range tx.All() {
					p := db.core.findLeaf(k).leaf
					if !p.mayContain(membershipHash(k)) {
						t.Fatal("published filter lost key")
					}
				}
				return nil
			}))
		}
	}
}

func TestMembershipKeyEquivalence(t *testing.T) {
	type namedFloat float64
	a, b := namedFloat(math.Float64frombits(0x7ff8000000000001)), namedFloat(math.Float64frombits(0xfff8000000000002))
	if membershipHash(a) != membershipHash(b) {
		t.Fatal("NaN hashes differ")
	}
	if membershipHash(namedFloat(0)) != membershipHash(namedFloat(math.Copysign(0, -1))) {
		t.Fatal("signed zero hashes differ")
	}
	type namedString string
	var db Tree[namedString, []int]
	stop := errors.New("rollback")
	mustTx(t, db.Update(func(tx *WriteTx[namedString, []int]) error {
		for _, k := range []namedString{"", "hello", "a\x00b", "世界"} {
			mustTx(t, tx.Put(k, []int{1}))
		}
		return nil
	}))
	if err := db.Update(func(tx *WriteTx[namedString, []int]) error {
		mustTx(t, tx.Put("hello", []int{2}))
		mustTx(t, tx.Delete("世界"))
		return stop
	}); err != stop {
		t.Fatal(err)
	}
	mustTx(t, db.View(func(tx *ReadOnlyTx[namedString, []int]) error {
		for _, k := range []namedString{"", "hello", "a\x00b", "世界"} {
			v, ok, err := tx.Get(k)
			mustTx(t, err)
			if !ok || v[0] != 1 {
				t.Fatal("named string restoration")
			}
		}
		return nil
	}))
}

func TestMembershipCollisionsUseExactLookup(t *testing.T) {
	db := NewBPTree[int, int](&tinyConfig)
	loadTx(t, db, 100)
	tx, _ := db.BeginUpdate()
	defer tx.Rollback()
	for n := db.core.firstLeaf(); n != nil; n = n.next {
		for i := range n.leaf.membership {
			n.leaf.membership[i] = math.MaxUint64
		}
	}
	// Deliberately force every probe to be a possible match. No false positive
	// may turn a fresh insertion into an overwrite or corrupt an undo record.
	for k := 0; k < 200; k++ {
		mustTx(t, tx.Put(k, -k))
	}
	if tx.Len() != 200 {
		t.Fatal("collision count")
	}
	mustTx(t, tx.Rollback())
	want := map[int]int{}
	for k := 0; k < 100; k++ {
		want[k] = k * 10
	}
	assertBindings(t, db, want)
}

func TestReusedJournalClearsReferences(t *testing.T) {
	var db Tree[int, *int]
	tx, _ := db.BeginUpdate()
	for k := 0; k < 1500; k++ {
		v := k
		mustTx(t, tx.Put(k, &v))
	}
	mustTx(t, tx.Commit())
	if cap(db.undoBuffer) == 0 {
		t.Fatal("expected reusable batch scratch")
	}
	for _, u := range db.undoBuffer[:cap(db.undoBuffer)] {
		if u.value != nil || u.key != 0 || u.kind != undoAbsent {
			t.Fatal("journal retains discarded state")
		}
	}
	saved := tx
	tx, _ = db.BeginUpdate()
	if len(tx.s.undo) != 0 {
		t.Fatal("new journal not empty")
	}
	if err := saved.Put(0, nil); err != ErrTxClosed {
		t.Fatal("old handle reused")
	}
	mustTx(t, tx.Delete(0))
	_, err := tx.Clear()
	mustTx(t, err)
	mustTx(t, tx.Put(1, nil))
	_, err = tx.Clear()
	mustTx(t, err)
	mustTx(t, tx.Rollback())
	if len(tx.s.clears) != 0 || len(tx.s.undo) != 0 {
		t.Fatal("closed transaction retains undo images")
	}
	mustTx(t, db.View(func(tx *ReadOnlyTx[int, *int]) error {
		if tx.Len() != 1500 {
			t.Fatal("restore length")
		}
		for k, v := range tx.All() {
			if v == nil || *v != k {
				t.Fatal("restore pointer binding")
			}
		}
		return nil
	}))
}

func TestInlineUndoReleasesAfterGrowth(t *testing.T) {
	var db Tree[int, *int]
	old := 7
	mustTx(t, db.Update(func(tx *WriteTx[int, *int]) error { return tx.Put(0, &old) }))
	tx, _ := db.BeginUpdate()
	for k := 0; k < 200; k++ {
		mustTx(t, tx.Put(k, nil))
	}
	mustTx(t, tx.Commit())
	if tx.firstUndo[0].value != nil {
		t.Fatal("closed handle retains copied inline undo")
	}
}
