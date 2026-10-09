// Package bufftree implements generic in-memory BP-trees with buffered,
// partitioned leaves. Tree iterates by key.
// Access is transaction-only: concurrent ReadOnlyTx instances see stable state;
// a WriteTx has exclusive access and supports real in-memory rollback.
// Shared reads never rearrange storage. Stored reference-valued data must be
// immutable; replace bindings through WriteTx.Put. Never nest transactions on
// the same database. Pass the existing transaction to callbacks and helpers.
package bufftree

// Config measures node sizes in entries, not bytes. Zero numeric fields use defaults.
// The defaults use a 42-slot log, 32-slot header, and 34-slot blocks. Fanout is
// independent of the leaf size. Key/value sizes affect the actual byte footprint.
type Config struct {
	Fanout    int
	LogSize   int
	NumBlocks int
	BlockSize int
}

func (c Config) normalized() Config {
	if c.Fanout == 0 {
		c.Fanout = 256
	}
	if c.LogSize == 0 {
		c.LogSize = 42
	}
	if c.NumBlocks == 0 {
		c.NumBlocks = 32
	}
	if c.BlockSize == 0 {
		c.BlockSize = 34
	}
	if c.Fanout < 3 || c.LogSize < 2 || c.NumBlocks < 2 || c.BlockSize < 2 {
		panic("bufftree: fanout must be >= 3; log, block count, and block size must be >= 2")
	}
	// Check arithmetic before allocating the contiguous leaf array.
	max := int(^uint(0) >> 1)
	if c.LogSize > max-c.NumBlocks || c.NumBlocks > (max-c.LogSize-c.NumBlocks)/c.BlockSize {
		panic("bufftree: leaf capacity overflow")
	}
	return c
}
