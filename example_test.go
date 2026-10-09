package bufftree_test

import (
	"fmt"

	"github.com/glycerine/bufftree"
)

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
