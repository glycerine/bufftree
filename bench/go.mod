module github.com/glycerine/bufftree/bench

go 1.26.4

require (
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/glycerine/bufftree v0.0.0
	github.com/glycerine/rbtree v0.2.2
	github.com/tidwall/btree v1.8.1
)

replace github.com/glycerine/bufftree => ..
