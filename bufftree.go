// Package bufftree implements generic in-memory BP-trees with buffered,
// partitioned leaves. Tree iterates by key; Dict iterates by insertion order.
// Trees, dictionaries, and their iterators require external synchronization
// when used by multiple goroutines. Ordered reads may sort leaf blocks.
package bufftree

// Config measures node sizes in entries, not bytes. Zero numeric fields use defaults.
// The defaults follow the paper's 32-slot log, header, and blocks. Fanout is
// independent of the leaf size. Key/value sizes affect the actual byte footprint.
type Config struct {
	Fanout    int
	LogSize   int
	NumBlocks int
	BlockSize int
}

func (c Config) normalized() Config {
	if c.Fanout == 0 {
		c.Fanout = 64
		//c.Fanout = 3
	}
	if c.LogSize == 0 {
		c.LogSize = 32
		//c.LogSize = 2
	}
	if c.NumBlocks == 0 {
		c.NumBlocks = 32
		//c.NumBlocks = 2
	}
	if c.BlockSize == 0 {
		c.BlockSize = 32
		//c.BlockSize = 2
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
