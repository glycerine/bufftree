module github.com/glycerine/bufftree/bench

go 1.26.4

require (
	github.com/glycerine/bufftree v0.0.1
	github.com/glycerine/insdict v0.14.1
	github.com/glycerine/rbtree v0.2.2
	github.com/tidwall/btree v1.8.1
)

require github.com/cespare/xxhash/v2 v2.3.0 // indirect

replace github.com/glycerine/bufftree => ..
