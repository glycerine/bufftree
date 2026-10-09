package bufftree_test

import (
	"fmt"
	"github.com/glycerine/bufftree"
)

func ExampleTree() {
	db := bufftree.NewBPTree[int, string](nil)
	err := db.Update(func(tx *bufftree.WriteTx[int, string]) error {
		for k, v := range map[int]string{30: "thirty", 10: "ten", 20: "twenty"} {
			if err := tx.Put(k, v); err != nil {
				return err
			}
		}
		for key, value := range tx.All() {
			fmt.Println(key, value)
			if err := tx.Delete(key); err != nil {
				return err
			}
		}
		fmt.Println("remaining:", tx.Len())
		return nil
	})
	if err != nil {
		panic(err)
	}
	// Output:
	// 10 ten
	// 20 twenty
	// 30 thirty
	// remaining: 0
}

func ExampleTree_View() {
	var db bufftree.Tree[int, int]
	_ = db.Update(func(tx *bufftree.WriteTx[int, int]) error {
		for i := 0; i < 10; i++ {
			if err := tx.Put(i, i*i); err != nil {
				return err
			}
		}
		return nil
	})
	_ = db.View(func(tx *bufftree.ReadOnlyTx[int, int]) error {
		tx.Range(3, 6, func(k, v int) bool { fmt.Println(k, v); return true })
		return nil
	})
	// Output:
	// 3 9
	// 4 16
	// 5 25
}
