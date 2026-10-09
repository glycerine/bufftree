package bench

import (
	"github.com/glycerine/bufftree"
	"iter"
)

// One complete transaction per operation, including journal creation and
// commit preparation. This measures the public API, not the private tree core.
type benchTree struct {
	db *bufftree.Tree[uint64, uint64]
}

func newBenchTree(cfg *bufftree.Config) *benchTree {
	return &benchTree{bufftree.NewBPTree[uint64, uint64](cfg)}
}
func (m *benchTree) Put(k, v uint64) {
	if err := m.db.Update(func(tx *bufftree.WriteTx[uint64, uint64]) error { return tx.Put(k, v) }); err != nil {
		panic(err)
	}
}
func (m *benchTree) Get(k uint64) (v uint64) {
	err := m.db.View(func(tx *bufftree.ReadOnlyTx[uint64, uint64]) error { var err error; v, _, err = tx.Get(k); return err })
	if err != nil {
		panic(err)
	}
	return
}
func (m *benchTree) Len() (n int) {
	_ = m.db.View(func(tx *bufftree.ReadOnlyTx[uint64, uint64]) error { n = int(tx.Len()); return nil })
	return
}
func (m *benchTree) Scan(k uint64, n int, fn func(uint64, uint64) bool) {
	_ = m.db.View(func(tx *bufftree.ReadOnlyTx[uint64, uint64]) error { tx.Scan(k, n, fn); return nil })
}
func (m *benchTree) All() iter.Seq2[uint64, uint64] {
	return func(yield func(uint64, uint64) bool) {
		_ = m.db.View(func(tx *bufftree.ReadOnlyTx[uint64, uint64]) error {
			for k, v := range tx.All() {
				if !yield(k, v) {
					break
				}
			}
			return nil
		})
	}
}

func (m *benchTree) PutBatch(keys []uint64, offset uint64) {
	err := m.db.Update(func(tx *bufftree.WriteTx[uint64, uint64]) error {
		for i, k := range keys {
			if err := tx.Put(k, offset+uint64(i)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
}
