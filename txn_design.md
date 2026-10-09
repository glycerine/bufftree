# Transaction API for the BP-tree

This design replaces the earlier per-operation snapshot proposal in `locks_design.md`. Every lookup, mutation, count, and traversal runs through an explicit transaction. A `ReadOnlyTx` holds one database-wide shared lock; a `WriteTx` holds that same lock exclusively. Transaction methods and iterators use the lock already owned by their transaction.

Read-only transactions can run concurrently. One write transaction excludes all other transactions, sees its own writes, and can stream through the table while deleting entries. Scans invoke callbacks as they advance, without first copying the result range. The lock remains held for the transaction's entire lifetime.

The API pattern comes from [reference/tx.go](reference/tx.go), [reference/iter.go](reference/iter.go), and the `KVcloser` declaration in [reference/db.go](reference/db.go). Reimplement that pattern using the BP-tree. Do not import the reference storage engine, caches, WAL, prefetch implementation, large/small value distinction, or persistence machinery.

**Rollback is real in-memory rollback**, as requested. An `Update` callback returning an error restores the transaction's starting logical contents. Commit publishes the transaction's changes to subsequent readers; it does not imply disk durability.

## 1. Database and lock ownership

Keep `Tree[K cmp.Ordered, V any]` and `NewBPTree` as the database-owning type and constructor. Move the current mutable tree implementation behind an unexported `treeCore`. The intended public API is transaction-only; this is an intentional API change, not an additional optional wrapper around unlocked public methods.

```go
type Tree[K cmp.Ordered, V any] struct {
    mu   sync.RWMutex
    core treeCore[K, V]
}

func (db *Tree[K, V]) View(fn func(*ReadOnlyTx[K, V]) error) error
func (db *Tree[K, V]) Update(fn func(*WriteTx[K, V]) error) error
func (db *Tree[K, V]) BeginView() *ReadOnlyTx[K, V]
func (db *Tree[K, V]) BeginUpdate() (*WriteTx[K, V], error)

func (tx *ReadOnlyTx[K, V]) Close()
func (tx *WriteTx[K, V]) Commit() error
func (tx *WriteTx[K, V]) Rollback() error
```

There is one lock acquisition at transaction entry and one corresponding release at transaction exit. No transaction method, iterator operation, `KVcloser.Close`, callback traversal helper, or BP-tree helper acquires this lock again. No per-node locks, snapshot gate, lock upgrades, or lock downgrades are needed.

The zero-value database is an empty, read-ready tree. Normalize its configuration under the first write transaction if necessary; a `View` of an empty zero-value tree must not perform lazy initialization. An explicitly constructed tree validates and copies its configuration before publication. Do not copy a database after first use.

Remove direct exported `Tree.Put`, `Get`, `Get2`, `Del`, `Len`, `Clear`, `Scan`, `Range`, `MapRange`, `All`, and raw iterator creation from the target API. Migrate callers to transactions. Do not retain convenience methods that silently begin transactions: they would make accidental recursive locking inside callbacks easy. Internal helpers retain the existing algorithms where suitable but operate on `treeCore` with an established transaction capability.

## 2. Read and write transaction surfaces

Use concrete transaction pointers in `View` and `Update` callbacks, as in the reference. Interfaces can document the common surface without forcing interface dispatch on every operation.

```go
type ReadOnlyDB[K cmp.Ordered, V any] interface {
    Get(key K) (value V, found bool, err error)
    GetKV(key K) (*KVcloser[K, V], error)
    Find(smod SearchModifier, key K) (*KVcloser[K, V], bool, error)
    FindIt(smod SearchModifier, key K) (*KVcloser[K, V], bool, error, *Iter[K, V])
    NewIter() *Iter[K, V]
    Ascend(pivot K, visit func(K, V) bool)
    Descend(pivot K, visit func(K, V) bool)
    AscendRange(greaterOrEqual, lessThan K, visit func(K, V) bool)
    DescendRange(lessOrEqual, greaterThan K, visit func(K, V) bool)
    Len() int64
}

type WritableDB[K cmp.Ordered, V any] interface {
    ReadOnlyDB[K, V]
    Put(key K, value V) error
    Delete(key K) error
    DeleteRange(begKey, endKey K, begInclusive, endInclusive bool) (n int64, allGone bool, err error)
    Clear() (allGone bool, err error)
    Merge(key K, fn func(old V, exists bool) (newValue V, write bool, doDelete bool)) error
    Commit() error
    Rollback() error
}
```

`ReadOnlyTx` implements only the read surface. `WriteTx` implements both. Mutations are not available on a read-only handle. An optional `WriteTx.Del` alias can ease migration from the existing BP-tree spelling; it delegates to `Delete` and has identical journaling and invalidation behavior.

Keep search modes `Exact`, `GTE`, `LTE`, `GT`, and `LT` with the reference's meanings and numeric values 0 through 4. Reject unknown modifiers. Use `compareKey`/`equalKey` throughout so named floating-point keys, NaNs, and signed zeros behave consistently with the current BP-tree.

Adaptations from the reference are explicit:

| Reference feature | BP-tree API decision |
| --- | --- |
| String keys and byte values | Generic `K` and `V`, preserving the BP-tree comparator. |
| `Get` returns value, found, value type, HLC, error | Return `V`, found, error. Application metadata can be part of `V`. |
| `Put` returns HLC and error | Return error; no persistence timestamp. |
| `Len` | Retain `int64` on transactions. |
| Large/small flags, `FetchLarge`, `FetchV`, `Large`, `LenBigSmall`, `Vin`, `Vel`, lazy flags | Omit; there is one value representation. Add `Iter.Value() V`. |
| Empty string means an unbounded endpoint | Do not use a zero-value sentinel for generic keys. The shown range methods have real, bounded endpoints. Use `SeekFirst`, `SeekLast`, or `Clear` for the corresponding unbounded operations. Empty strings and numeric zero remain valid keys. |
| `Clear`/`DeleteRange` can reset the rollback baseline | Do not copy that storage-specific exception. These operations remain fully rollbackable. `allGone` reports that the resulting logical database is empty. |
| Cache-pinning `KVcloser` | Preserve the result/Close API using transaction-owned result records; no cache or file pinning. |

`Ascend` visits keys at or above its pivot; `Descend` visits keys at or below its pivot. `AscendRange` uses `[lower,upper)`; `DescendRange` uses `(lower,upper]`. `DeleteRange` uses its explicit inclusivity flags, skips empty/inverted intervals, and counts only live keys actually removed. `Clear` reports `allGone=true` after successful clearing, including an already empty tree.

`Merge` calls its function once with the current value and presence bit. Both action flags false means no change; `write` means `Put`; `doDelete` means `Delete`; both true returns an error, matching the reference. Nested calls on the same transaction are permitted. Before applying the returned action, recheck that the transaction remains open and journal the value present immediately before that action.

Port the existing `Range`, `Scan`, `MapRange`, and `All` conveniences onto transaction handles if compatibility requires them. They must use this same streaming iterator machinery and never begin another transaction. For the initial implementation, `MapRange` may visit in sorted order because unspecified order permits that; it must not copy a leaf's results merely to survive callback mutation.

## 3. Streaming use, including deletion

```go
err := db.View(func(tx *bufftree.ReadOnlyTx[int, Record]) error {
    it := tx.NewIter()
    defer it.Close() // optional; transaction exit also closes it
    it.Seek(100)
    for n := 0; it.Valid() && n < 100; n++ {
        consume(it.Key(), it.Value())
        it.Next()
    }
    return nil
})
```

The first entry can be consumed as soon as the cursor reaches it. There is no preliminary copy of 100 entries and no lock operation in `Next`.

```go
err := db.Update(func(tx *bufftree.WriteTx[int, Record]) error {
    it := tx.NewIter()
    defer it.Close()
    for it.SeekFirst(); it.Valid(); it.Next() {
        key, value := it.Key(), it.Value()
        if obsolete(value) {
            if err := tx.Delete(key); err != nil {
                return err // all deletions in this transaction roll back
            }
        }
    }
    return nil // commit
})
```

Deletion may merge leaves or collapse the root. On the next step, the iterator detects the mutation and seeks strictly beyond its saved key in the current tree. It must not dereference its old cursor first.

Callbacks may perform nested reads, scans, and writes **through the same transaction**, subject to its read/write capability. They must not call `db.View`, `db.Update`, `BeginView`, or `BeginUpdate` on that same database while the transaction is active. Even nesting a read transaction inside a read transaction is unsafe when a writer is waiting. Pass the existing transaction to helpers instead.

A transaction and its iterators are used sequentially by one goroutine at a time. Separate read-only transactions can run concurrently; sharing a `WriteTx` across concurrent goroutines would bypass the serialization intended by its exclusive lock. Callbacks must not wait for other work that needs to acquire the same database lock.

## 4. Transaction lifetime and terminal behavior

Use a private transaction state containing the owner, mode, open/committed/rolled-back/closed state, managed/manual status, active iterator/result registry, and write-transaction preparation/journal state. Handles and resources refer to this state. Do not embed `ReadOnlyTx` directly in `WriteTx` and accidentally expose a `Close` that performs `RUnlock` on a write lock.

| Operation | Semantics |
| --- | --- |
| `View(fn)` | Acquire `RLock`, run `fn`, invalidate/close transaction resources, and release `RLock` on return or panic. Return the callback's error. |
| `Update(fn)` returning nil while open | Prepare and commit, close resources, then unlock. |
| `Update(fn)` returning an error while open | Undo all writes, restore a read-ready tree, close resources, unlock, and return the error. |
| Panic while an `Update` remains open | Roll back and clean up before unlocking; let the original panic continue. |
| `BeginView` | Caller owns the shared-lock lifetime and must call `Close`. |
| `BeginUpdate` | Caller must call `Commit` or `Rollback`; idiomatically defer `Rollback` immediately. |
| Manual `Commit`/`Rollback` | Terminal and release the write lock after finalization. First terminal action wins; subsequent terminal calls are no-ops, making deferred rollback safe. |
| Manual `ReadOnlyTx.Close` | Terminal, idempotent, and releases the shared lock exactly once. |

Match the reference's managed write-transaction distinction: explicitly calling `Commit` or `Rollback` inside `Update` makes the handle terminal, but the outer `Update` retains lock ownership until its callback unwinds. The wrapper must not finalize or unlock twice. An error or panic after explicit commit does not undo the already chosen commit; callers should return immediately after an explicit terminal action. Prefer automatic finalization in managed callbacks.

Apply the same ownership distinction if a callback explicitly closes its `ReadOnlyTx`: mark it unusable and close its resources, but let `View` perform its single `RUnlock` when the callback unwinds. This avoids the double-unlock hazard of mechanically copying the reference's simple read-only `Close` implementation.

Error-returning methods reject a terminal handle with a closed-transaction error. Non-error query constructors/helpers such as `Len`, `NewIter`, and traversal methods panic with that error when called on a closed transaction. An already closed iterator is invalid; movement is a no-op, `KV` returns nil, and `Key`/`Value` return zero values. Cleanup operations remain idempotent.

After any user callback, a helper must recheck transaction state before continuing. If the callback ended the transaction, traversal stops without further tree access; a pending `Merge` action reports the closed-transaction error. Never keep walking a borrowed run after a callback without checking both transaction lifetime and cursor invalidation.

Use deferred cleanup that rolls back any still-open managed write transaction even if the callback does not return normally, including `runtime.Goexit`. Do not rely solely on `recover()` returning a non-nil value. Register manual cleanup immediately after `BeginUpdate`; a leaked manual transaction intentionally holds its lock until the caller ends it.

Rollback/panic guarantees cover valid tree states and ordinary errors or user panics between completed core operations. They do not promise recovery from fatal allocation failure or an internal corruption bug. Do not publish a partially restored tree if an invariant check fails during finalization.

## 5. Make shared transactions physically read-only

The current BP-tree cannot simply be wrapped in `RLock`: `Len` reconciles counts, `cursor`/`scanCursor` sort logs and blocks, `prepareScan` may flush, and `mapEntries` sorts the log. Concurrent readers executing those paths would race even though each holds a shared transaction lock.

Establish a **read-ready publication invariant** before releasing any write transaction, whether committed or rolled back:

- Every reachable leaf's live log and blocks are sorted according to the existing comparator; sortedness flags and header mirrors describe the actual storage.
- Tombstones and buffered duplicates are represented consistently. The sorted log still overrides matching base records, including deletion shadows.
- Leaf membership accounting and the global length are exact; no pending reconciliation remains for a reader.
- Root, separators, minima, parent links, and both leaf links are structurally consistent.
- No subsequent read needs to write a sortedness flag, cache bit, counter, version, buffer-pool entry, or tree-owned scratch area.

This does **not** require flushing every log on every commit. Sort the remaining log and any dirty blocks, retain buffered entries, and use a read-only merge cursor. Preserve BPA buffering where it is compatible with the invariant. There is a write-side preparation cost, amortized across a batch of writes in one transaction, that must be measured.

Separate preparation from traversal:

| Context | Permitted behavior |
| --- | --- |
| `ReadOnlyTx` | Use pure lookup and read-ready cursors. Update only transaction/iterator-local state. Never repair an unprepared leaf under `RLock`. |
| `WriteTx` mutation | Use existing buffered mutation and rebalancing algorithms, record touched leaves, and invalidate affected cursor layouts. |
| `WriteTx` lookup/scan | See current writes immediately. Before an ordered cursor enters a dirty leaf, prepare it under the existing exclusive lock; then use the same pure cursor machinery. |
| Commit or rollback completion | Prepare all reachable touched leaves and reconcile counts before publication. |

Refactor `bpa.cursor`, `advanceBase`, `scanBlock`, `scanCursor`, `nextRun`, and `mapEntries` so the read-only variants contain no sorting or writes, including hidden writes on later leaf/block transitions. Move `prepareScan`'s flushing and `scanReady` updates into writer-only preparation or remove that heuristic from read transactions. `ReadOnlyTx.Len` reads an already exact count. `WriteTx.Len` may reconcile its current dirty accounting while exclusively locked.

Initially, a writer can verify/prep by traversing all reachable leaves for correctness. Before relying on commit cost proportional to changed leaves, implement and test a separate writer-owned preparation set: mark every mutated/new leaf, including both sides of redistribution; discard or skip retired nodes; do not confuse this set with the count-reconciliation list, which a `WriteTx.Len` call may empty earlier. Publication must never overlook an unsorted surviving leaf.

Keep `rebuildBuffer` and other mutable tree scratch writer-only. Read cursors use their own small state; no shared checkout/return of `mapBuffers` occurs in a read transaction. Avoid a shared scratch pool in the first implementation. Transaction-local handle registration and allocation are permitted; they do not modify the shared BP-tree.

## 6. Iterators and mutation recovery

Preserve the reference's iterator positioning convention, which differs from the existing BP-tree iterator:

```go
func (it *Iter[K, V]) Seek(target K) // first key >= target
func (it *Iter[K, V]) SeekFirst()
func (it *Iter[K, V]) SeekLast()
func (it *Iter[K, V]) Next()
func (it *Iter[K, V]) Prev()
func (it *Iter[K, V]) Valid() bool
func (it *Iter[K, V]) Key() K
func (it *Iter[K, V]) Value() V
func (it *Iter[K, V]) KV() *KV[K, V]
func (it *Iter[K, V]) Close()
```

`NewIter` starts unpositioned. `Seek*` positions immediately, and callers test `Valid`. `Next` and `Prev` do not return a boolean or automatically initialize an invalid iterator. Reaching an end remains invalid until an explicit seek. Closing an iterator never releases the transaction's database lock. All outstanding iterators are closed at transaction end.

For `ReadOnlyTx`, the logical contents and physical layout stay fixed for the entire transaction. Iterators may retain node pointers and cursor offsets without copying the range, validation against concurrent writers, or root restarts. Implement forward and reverse merging of sorted log/base records; log versions and tombstones win in both directions. `Prev`/`SeekLast` require real reverse BP-tree support rather than collecting a whole table into a slice.

For `WriteTx`, multiple iterators may coexist with mutations and nested scans. Each iterator maintains its own copied current key/value and a saved anchor key, plus a cursor and a mutation/layout epoch. The current pair denotes the last positioned result; deleting or replacing its key does not turn it into a reference to a moving array slot.

Increment the write transaction's epoch before any operation that can invalidate positions: logical mutation, split/merge, redistribution, `Clear`, or preparation that moves records. Preparation by one iterator can invalidate another even without changing logical contents. Use ordinary protected integers, not atomics; transaction handles are sequential. Handle counter wrap by explicitly invalidating all registered cursor states before reusing an epoch value.

On `Next` or `Prev`, compare the epoch **before** using any saved node, slice, or offset. If stale, discard the cursor and seek from the current root using the saved anchor: strictly greater for `Next`, strictly less for `Prev`. Copy the resulting current pair into iterator-owned storage. A direction change also positions relative to that current anchor.

Consequences for write-transaction iteration:

- Deleting the current key and then calling `Next` visits its current successor without using the removed node.
- Inserting keys ahead of the anchor can make them visible; inserting keys behind it does not revisit them during a forward pass. Reverse traversal is symmetric.
- Other iterators see the transaction's changes when they move or explicitly seek. Their previously returned current pair is a saved observation, not a promise of current membership.
- `Clear` invalidates cached positions. A subsequent step seeks beyond the saved anchor in the now-current tree; with no subsequent inserts it becomes invalid. Explicit `SeekFirst` restarts from the beginning.
- Nested traversal or callbacks that mutate must trigger the same invalidation checks in outer traversals, including any optimized contiguous-run path.

This is a consistent live view of the transaction's own writes, not an immutable snapshot of the write transaction's starting contents. A read-only transaction does provide that fixed view because there is no writer. A callback that continually inserts new keys ahead of itself can extend a write scan indefinitely; lock fairness cannot make self-extending application work terminate.

## 7. `KV`, `KVcloser`, and ownership

```go
type KV[K cmp.Ordered, V any] struct {
    Key   K
    Value V
}

type KVcloser[K cmp.Ordered, V any] struct {
    KV[K, V]
    // private transaction/resource lifetime state
}

func (kv *KVcloser[K, V]) Close()
```

Retain the reference's actual `KVcloser` spelling for API familiarity. It has no database-lock ownership. `Close` is nil-safe and idempotent, clears its exposed record, and unregisters its transaction-local resource state. There is no large-value fetch operation or cache pin.

`GetKV` is `Find(Exact, key)`. A miss returns nil without an error. `Find` returns an independent record holder and an `exact` flag based on the BP-tree comparator. `FindIt` retains the reference's result order `(kv, exact, err, it)` and additionally returns an iterator positioned at the match. On an ordinary miss, it returns nil `kv` and an open, invalid iterator; on an invalid modifier or closed transaction, return an error without leaving a live registered iterator.

A `FindIt` result is independent of subsequent iterator motion. Construct it by copying one key/value pair, not by exposing a pointer into leaf storage. The transaction closes outstanding holders at its end, and callers can close them earlier. `Iter.KV` returns an iterator-owned record, valid until movement, iterator close, or transaction end; callers must treat it as read-only. Never expose a mutable `*entry` from the BP-tree.

`Get`, `Key`, and `Value` copy Go values normally. Reference-containing values are not deep copied. Stored referents must be treated as immutable by this API, including inside a `WriteTx`; replace the binding with `Put` to make a rollbackable change. Mutating a map or slice obtained through `Get` would bypass both the read-only capability and the undo journal. Supporting mutable object graphs would require a separate cloning/ownership contract.

Register only active resources, using an unlinkable transaction-local registry or equivalent. Closing a holder or iterator removes its reference from that registry. A long scan performing repeated `Find`/`Close` must not retain all previously closed results until transaction end.

## 8. Real in-memory rollback

Apply write operations immediately to the private core while the `WriteTx` holds the exclusive lock. Reads in that transaction see the changes; other transactions cannot observe them before completion. Record sufficient undo information before each logical mutation, and replay inverses in reverse order on rollback.

Start with a chronological operation journal rather than a first-write hash map. Generic keys include NaNs, so Go's `map[K]` equality would not match the BP-tree's key equivalence. A chronological journal naturally handles repeated writes to a key and avoids introducing a second key-indexing scheme.

For each actual `Put` or `Delete`, record whether an equivalent key was present and, if so, its prior stored key and value. Restore using raw, non-journaling operations: remove the current equivalent binding, then reinsert the prior stored binding if it existed. This also preserves the stored key representation when equivalent keys have different encodings. Tree shape and buffer arrangement need not match their pre-transaction layout; logical bindings and public invariants must match.

`DeleteRange` streams and journals each successful deletion. `Clear` records an owned image of the current live bindings before clearing; its inverse clears the current contents and restores that image. A sequence of puts, clears, and later writes rolls back correctly by reverse replay. No clear operation silently changes the original rollback baseline.

Record before changing logical contents; a journaling failure cannot leave an unrecorded mutation. Helpers used during replay must bypass journaling and user callbacks. Invalidate/close active iterators before terminal replay. Reconcile counts and restore read-ready layout after replay, then release the lock. The journal holds logical records rather than pointers to mutable nodes or entry slots.

There is no full-database copy at transaction entry or before a scan. Memory is proportional to journaled write history; `Clear` needs an image of the contents it removes. A full-table scan that deletes every key can therefore consume memory proportional to the deleted data **for rollback**, even though the scan itself streams with bounded cursor state. This is the cost of the requested rollback semantics. Optimize journal coalescing or root/version retention only with a separate proof later.

The journal restores tree bindings, not arbitrary external mutations through a stored reference-valued `V`. The immutable-referent rule above is part of the rollback contract.

## 9. Publication, isolation, and progress

Hold the database lock through user callbacks, iterator cleanup, commit preparation, and rollback restoration. For a successful write, the committed logical state and read-ready physical state are both established before `Unlock`. A subsequent read transaction's `RLock` supplies the memory visibility required to consume them.

A read transaction's state is the committed state at its successful lock acquisition and remains stable until it closes. Concurrent read transactions may overlap. An exclusive write transaction occupies one place between read epochs and other writers; its own calls can observe intermediate writes, but no other transaction can. A rollback contributes no committed logical change.

This gives transaction isolation and atomic publication without allocating a range snapshot. It does not imply durability. Returning an error from `View` simply ends that read scope; returning an error from an open `Update` triggers actual rollback.

Assume the standard library's reader/writer admission progress and scheduler progress; do not assume strict FIFO acquisition. With one database lock and no reacquisition from transaction methods, there is no internal multi-lock cycle. Writer progress still requires readers and previous writers to finish their transactions. Long-running callbacks or leaked manual transactions hold the lock by design; no implementation can guarantee bounded writer latency while allowing arbitrary transaction duration.

## 10. Proof obligations

The earlier `locks.lean` modeled the paper and a superseded snapshot gate. The transaction-specific model is now [txn.lean](txn.lean). It proves abstract capability exclusion, read-ready publication, whole-span stability, no internal wait cycles, terminal idempotence, and reverse journal restoration. Concrete BPA enumeration, key equivalence, and Go lifecycle refinement remain implementation verification obligations tested in [tx_test.go](tx_test.go), not machine-checked Go proofs. The full obligation list is:

| Obligation | Planned statement |
| --- | --- |
| Ownership and capabilities | Active read transactions own shared access; an active write transaction owns exclusive access. Only `WriteTx` can change logical or physical tree state. |
| Shared-reader safety | Every read operation preserves shared tree storage, including layout, metadata, sortedness, count state, and scratch ownership. |
| Publication invariant | Initial state, commit, and completed rollback are read-ready before an external reader can enter. Writer preparation preserves logical contents. |
| Whole-span read consistency | Across any active `ReadOnlyTx`, the logical map and iterator-visible layout are constant. Complete scans match that map exactly, with correct range bounds and no duplicate live keys. |
| No reacquisition | Transaction operations and iterator/result close operations never request the owning database lock again. Nested same-transaction helpers preserve the ownership capability. |
| Resource lifetime | Transaction completion invalidates all owned iterators/results before unlock; no operation touches core storage through a closed capability. Managed and manual cleanup each unlock exactly once. |
| Read-your-writes | Reads and new iterator positions inside `WriteTx` observe its current logical map. Mutation/preparation invalidates stale cursor state before reuse. |
| Iterator recovery | Forward/backward recovery uses strict comparator order from an owned anchor after any layout change, including deletion of the current key and nested iterator preparation. |
| Rollback | Reverse journal replay restores the transaction's entry logical map and exact length, including repeated keys, NaNs, `Clear`, range deletion, and structural changes. |
| Commit/rollback terminality | First terminal action wins; managed completion preserves outer lock ownership until unwind. Error/panic cleanup cannot publish partial changes or double-unlock. |
| Conditional progress | Given admission progress and finite transaction work, transactions complete. Standard lock progress excludes the old unfair-lock negative control; application callbacks and journal replay have explicit termination premises. |

Begin with abstract logical maps, transaction modes, journals, and lifecycle states. Then refine the publication invariant and pure cursor behavior against the BP-tree layout. Keep sequential key ordering, exact enumeration, split/merge correctness, and rollback correctness separate from mutual exclusion: the lock alone does not prove those properties.

## 11. Implementation and validation sequence

1. **Introduce the transaction-only surface.** Refactor current operations behind `treeCore`; add the single owner lock, transaction state, managed/manual entry and exit, and capability checks. Migrate examples, tests, and benchmarks to `View`/`Update` rather than retaining hidden auto-transactions.
2. **Separate writer preparation from pure reading.** Add forward/reverse read-ready cursors and exact published counts. Instrument mutation paths to track all leaves needing publication work. Verify a committed tree can service concurrent read transactions without any shared data changes.
3. **Implement journaled writes and real rollback.** Include every mutation entry point and compound operation. Test repeated keys, replacement, absent deletion, multiple clears, range deletion, and logical restoration after structural rebalancing before exposing `Update` as complete.
4. **Implement the reference iterator/search surface.** Add `SeekFirst`, `SeekLast`, `Prev`, search modifiers, `FindIt`, independent `KVcloser` records, automatic resource cleanup, and safe write-iterator reseeking. Use streaming traversal for callback methods.
5. **Complete lifecycle and formal checks.** Cover managed explicit terminal calls, callback errors/panics, early close, use after close, and the transaction-specific Lean obligations. No `sorry` or custom progress axioms standing in for application proofs.
6. **Measure the tradeoffs.** Benchmark point operations in single-item and batched transactions, read-only concurrent scans, write scans with deletion, commit preparation, reverse iteration, and rollback. Report undo memory separately from cursor memory and distinguish acquisition wait time from time spent holding the lock.

Required tests include:

- Multiple `View` transactions traversing the same leaves concurrently; readers leave a captured physical tree image unchanged, excluding lock internals and their private handles.
- A pending writer waits for existing views and eventually enters after they close; new readers cannot perpetually postpone it under the library progress contract. Use channel coordination rather than sleeps to arrange events.
- Two-key and three-leaf write transactions never expose intermediate states to any `View`; a failed `Update` exposes none of its changes afterward.
- Full-table forward and reverse scans deleting current keys, forcing splits/merges/root collapse, and maintaining several active iterators; nested scans that prepare dirty writer leaves must not corrupt outer cursors.
- Exact `Find`/`GTE`/`GT`/`LTE`/`LT` and range boundaries, including zero keys, empty strings, named floats, multiple NaN encodings, signed zeros, and nil values distinguished from absence.
- Rollback after replacement, deletion, resurrection, range deletion, `Clear`, and repeated operations on equivalent keys; compare restored logical bindings and counts to the entry model.
- No full-result materialization for streaming scans and immediate early stop. Assert that a one-entry read does not traverse/copy the rest of the range. Account for intentional undo storage when mutations occur.
- Closed handles cannot touch storage; `KVcloser.Close` never unlocks the database; manual and managed completion cannot double-unlock; resource registration grows with live handles, not the number of handles ever created.
- Callback error/panic cleanup restores a valid read-ready tree, releases ownership, and leaves later transactions usable. Verify the explicitly committed-then-error case separately.
- `go test .`, `go vet .`, `go test -race .`, and the Lean checks. Use package-targeted Go commands because `reference/` is API-reading material, not an implementation package to build recursively.

The implementation is in [tx.go](tx.go), [iter.go](iter.go), and [api.go](api.go).
The mutable engine is private `treeCore`; existing algorithm tests retain direct
core access, while transaction tests and public benchmarks exercise the new API.
Preparation uses a separate intrusive leaf list from membership accounting;
retired leaves on that list may be harmlessly prepared and are released at
finalization. Streaming callback traversal consumes pure contiguous runs and
checks transaction lifetime and epoch (including wrap) after every callback.

The public API, rollback, preparation, and resource semantics above are implemented.
[txn.lean](txn.lean) proves the abstract protocol and journal results described
above, with no `sorry` or custom axioms; it is not a proof of the compiled Go code.
Prepared-state assertions belong at commit/rollback publication, not after each
individual write in an open batch.

## 12. Implemented batch optimizations

The public API and transaction semantics above remain unchanged. The writer's
required preimage lookup now also determines membership and supplies the leaf
for mutation, avoiding repeated descent. Existing bindings update in place;
new bindings enter a counted log. A conservative `logNoShadows` flag allows a
flush to skip block duplicate searches only when no log key shadows a live
base record. Unchecked core appends clear that fact when necessary.

A per-leaf blocked membership filter is an exact-lookup shortcut, never an
approximate public answer. Every insertion records its key; loads across leaves
rebuild the filter, while redistribution within a leaf preserves its superset.
Deleting keys need not clear bits. The filter uses `maphash.Comparable` with an
immutable process-local seed and a fixed hash for every NaN. This preserves
comparator equivalence, including named types and signed zeros. A negative probe
can bypass the exact lookup; a positive probe cannot establish presence. Tests
force collisions and check membership across splits, merges, and rollback.

Sorted-prefix metadata identifies the part of each log/block that still needs
insertion sorting. Deletion shortens the prefix when it swaps a final record
into an earlier position. Publication still leaves complete streams sorted.
Read transactions never update the filter, prefixes, or any shared bookkeeping.

Undo records store one key (the prior representative if present, the requested
key otherwise), the prior value, and an action tag. Clear images live in a
separate chronological stack; reverse replay consumes one image per Clear tag.
This preserves reverse journal semantics while making scalar undo records
pointer-free. After terminal replay/publication, clear all journal references,
including the inline first record. Reuse only cleared dynamically allocated
storage with capacity at most 4,096 records; never reuse transaction handles.

See `benchmark-results/2026-10-09-txn-opt/summary.md` for measured speed/memory
tradeoffs. This is an implementation refinement of the transaction protocol,
not a replacement for the concrete refinement obligations listed above.
