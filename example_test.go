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

func ExampleDict() {
	dict := bufftree.NewDict[string, int](nil)
	dict.Put("charlie", 3)
	dict.Put("alice", 1)
	dict.Put("bob", 2)
	dict.Put("alice", 10)
	it := dict.Iter()
	for it.Next() {
		fmt.Println(it.Key(), it.Value())
		it.Del()
	}
	// Output:
	// charlie 3
	// alice 10
	// bob 2
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
