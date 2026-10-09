// Package bufftree implements generic in-memory BP-trees with buffered,
// partitioned leaves. Tree iterates by key.
// Trees and their iterators require external synchronization
// when used by multiple goroutines. Ordered reads may sort or flush leaves.
package bufftree

// Config measures node sizes in entries, not bytes. Zero numeric fields use defaults.
// The defaults use a 32-slot log/header and 34-slot blocks. Fanout is
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
		c.LogSize = 32
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
