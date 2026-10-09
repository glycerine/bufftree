package bufftree_test

import (
	"fmt"
	"slices"

	"github.com/glycerine/bufftree"
)

func ExampleSingleWriterRWMutex_Downgrade() {
	var mu bufftree.SingleWriterRWMutex
	data := []int{3, 1, 2}

	mu.Lock()
	slices.Sort(data)
	mu.Downgrade() // Keep protection while admitting other readers.
	fmt.Println(data)
	mu.RUnlock() // Downgrade leaves one read hold, not a write hold.

	// Output: [1 2 3]
}

func ExampleTree() {
	tree := bufftree.NewBPTree[int, string](nil)
	tree.Put(30, "thirty")
	tree.Put(10, "ten")
	tree.Put(20, "twenty")
	for key, value := range tree.All() {
		fmt.Println(key, value)
		tree.Del(key)
	}
	fmt.Println("remaining:", tree.Len())
	// Output:
	// 10 ten
	// 20 twenty
	// 30 thirty
	// remaining: 0
}

func ExampleTree_Range() {
	var tree bufftree.Tree[int, int]
	for i := 0; i < 10; i++ {
		tree.Put(i, i*i)
	}
	tree.Range(3, 6, func(key, value int) bool {
		fmt.Println(key, value)
		return true
	})
	// Output:
	// 3 9
	// 4 16
	// 5 25
}
