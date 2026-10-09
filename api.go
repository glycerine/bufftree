package bufftree

import "cmp"

// ReadOnlyDB is the common read surface of both transaction types.
type ReadOnlyDB[K cmp.Ordered, V any] interface {
	Get(K) (V, bool, error)
	GetKV(K) (*KVcloser[K, V], error)
	Find(SearchModifier, K) (*KVcloser[K, V], bool, error)
	FindIt(SearchModifier, K) (*KVcloser[K, V], bool, error, *Iter[K, V])
	NewIter() *Iter[K, V]
	Ascend(K, func(K, V) bool)
	Descend(K, func(K, V) bool)
	AscendRange(K, K, func(K, V) bool)
	DescendRange(K, K, func(K, V) bool)
	Len() int64
}

// WritableDB adds rollbackable writes and terminal actions.
type WritableDB[K cmp.Ordered, V any] interface {
	ReadOnlyDB[K, V]
	Put(K, V) error
	Delete(K) error
	DeleteRange(K, K, bool, bool) (int64, bool, error)
	Clear() (bool, error)
	Merge(K, func(V, bool) (V, bool, bool)) error
	Commit() error
	Rollback() error
}

var _ ReadOnlyDB[int, int] = (*ReadOnlyTx[int, int])(nil)
var _ WritableDB[int, int] = (*WriteTx[int, int])(nil)
