import Std

/-!
# BP-tree locking audit

Source: the local `bptree-vldb-23.pdf`, sections 2.1 and 5.
Check with: lean +leanprover/lean4:v4.34.1 locks.lean
No Mathlib, added axioms, `sorry`, or native_decide are used.

This is a lock-protocol abstraction, not a verification of the C++ artifact or
of this repository's (externally synchronized) Go implementation. Node numbers
are lock-order ranks: ancestors precede descendants, leaves go left to right.
We prove results for every execution of the transition system, not just a
bounded enumeration. Allocation, deletion, root publication, memory reclamation,
callbacks, and the concrete RW-lock implementation are outside this model.

The paper's parent upgrade is a *try* upgrade: it must not block while keeping
the read lock. Its scan upgrade explicitly releases R before requesting W.
These distinctions are essential, and are modeled separately below.

EXTENDED FINDINGS (Go primitives, starvation, and whole-scan snapshots):
* GoPrimitives: upgrade-free Lock/RLock/Unlock/RUnlock safety and deadlock proof.
* SnapshotCounterexample: a legal three-leaf scan returns a state that NEVER
  existed. The paper's hand-over-hand discipline is insufficient for snapshot
  semantics, even without a split or upgrade gap.
* SnapshotGate: an additional RWMutex gate freezes the entire logical map for
  a scan and gives a snapshot-consistency proof for its emitted observations.
* Starvation: a valid infinite execution separates deadlock freedom from
  starvation freedom in the lock safety abstraction.
* FIFO: an explicit admission queue has post-enqueue starvation freedom under
  stated scheduling and finite-owner hypotheses. This is not an unconditional
  progress proof for Go's runtime, nor a verified Go channel implementation.

The original model is retained to audit the paper, including its try-upgrade;
the new GoPrimitives model deliberately has no such primitive. No Go tree
implementation is changed by this file.
-/

namespace BPTreeLocks

abbrev Thread := Nat
abbrev Node := Nat

inductive Mode where
  | read | write
  deriving DecidableEq, Repr

structure Request where
  node : Node
  mode : Mode
  deriving DecidableEq, Repr

structure State where
  held : Thread → Node → Option Mode
  waiting : Thread → Option Request

def empty : State := ⟨fun _ _ => none, fun _ => none⟩

def setHeld (s : State) (t : Thread) (n : Node) (m : Option Mode) : State :=
  { s with held := fun u k => if u = t ∧ k = n then m else s.held u k }

def setWait (s : State) (t : Thread) (r : Option Request) : State :=
  { s with waiting := fun u => if u = t then r else s.waiting u }

def releaseAll (s : State) (t : Thread) : State :=
  { held := fun u n => if u = t then none else s.held u n
    waiting := fun u => if u = t then none else s.waiting u }

def Conflicts (a b : Mode) : Prop := a = .write ∨ b = .write

def Compatible (s : State) (r : Request) : Prop :=
  ∀ u m, s.held u r.node = some m → ¬ Conflicts r.mode m

def Safe (s : State) : Prop :=
  ∀ t u n a b, s.held t n = some a → s.held u n = some b →
    Conflicts a b → t = u

def Ordered (s : State) : Prop :=
  ∀ t r, s.waiting t = some r →
    ∀ n m, s.held t n = some m → n < r.node

/-
Blocking acquisitions must follow every lock already held by the requesting
thread. This permits read/write coupling down the tree, forward leaf scans,
and pessimistic descents retaining ancestors. There is deliberately no
"at most two locks" bound: split propagation needs retained ancestor locks or
an additional restart/revalidation algorithm, which the prose does not supply.

tryUpgrade is atomic and nonblocking. It is allowed on an earlier (parent) node
while holding a later (leaf) node. On failure, retry releases ALL this thread's
locks. There is no waiting-upgrade transition in the sound protocol.

A scan conversion is release followed by request/write, then grant. It can
retain an earlier parent or leaf, but cannot retain a later node while waiting
to reacquire this one. No shared data may be accessed during the gap.
-/
inductive Step : State → State → Prop where
  | request (s : State) (t : Thread) (r : Request)
      (idle : s.waiting t = none)
      (order : ∀ n m, s.held t n = some m → n < r.node) :
      Step s (setWait s t (some r))
  | grant (s : State) (t : Thread) (r : Request)
      (pending : s.waiting t = some r)
      (available : Compatible s r) :
      Step s (setWait (setHeld s t r.node (some r.mode)) t none)
  | release (s : State) (t : Thread) (n : Node) :
      Step s (setHeld s t n none)
  | tryUpgrade (s : State) (t : Thread) (n : Node)
      (idle : s.waiting t = none)
      (reader : s.held t n = some .read)
      (sole : ∀ u m, s.held u n = some m → u = t) :
      Step s (setHeld s t n (some .write))
  | retry (s : State) (t : Thread) : Step s (releaseAll s t)

inductive Reachable : State → Prop where
  | initial : Reachable empty
  | next : Reachable s → Step s s' → Reachable s'

theorem empty_safe : Safe empty := by
  simp [Safe, empty]

theorem empty_ordered : Ordered empty := by
  simp [Ordered, empty]

theorem step_safe (hs : Safe s) (step : Step s s') : Safe s' := by
  cases step with
  | request => exact hs
  | grant t r pending available =>
    intro a b n ma mb ha hb conflict
    simp only [setWait, setHeld] at ha hb
    by_cases h₁ : a = t ∧ n = r.node
    · rw [ite_eq_left h₁] at ha
      have hma := Option.some.inj ha
      by_cases h₂ : b = t ∧ n = r.node
      · exact h₁.1.trans h₂.1.symm
      · simp only [h₂, ite_false] at hb
        have hn := h₁.2
        subst n
        subst ma
        exact False.elim (available b mb hb conflict)
    · simp only [h₁, ite_false] at ha
      by_cases h₂ : b = t ∧ n = r.node
      · rw [ite_eq_left h₂] at hb
        have hmb := Option.some.inj hb
        have hn := h₂.2
        subst n
        subst mb
        exact False.elim (available a ma ha (conflict.elim Or.inr Or.inl))
      · simp only [h₂, ite_false] at hb
        exact hs a b n ma mb ha hb conflict
  | release t n =>
    intro a b k ma mb ha hb conflict
    simp only [setHeld] at ha hb
    have haOld : s.held a k = some ma := by split at ha <;> simp_all
    have hbOld : s.held b k = some mb := by split at hb <;> simp_all
    exact hs a b k ma mb haOld hbOld conflict
  | tryUpgrade t n idle reader sole =>
    intro a b k ma mb ha hb conflict
    simp only [setHeld] at ha hb
    by_cases h₁ : a = t ∧ k = n
    · by_cases h₂ : b = t ∧ k = n
      · exact h₁.1.trans h₂.1.symm
      · simp only [h₂, ite_false] at hb
        exact h₁.1.trans (sole b mb (h₁.2 ▸ hb)).symm
    · simp only [h₁, ite_false] at ha
      by_cases h₂ : b = t ∧ k = n
      · exact (sole a ma (h₂.2 ▸ ha)).trans h₂.1.symm
      · simp only [h₂, ite_false] at hb
        exact hs a b k ma mb ha hb conflict
  | retry t =>
    intro a b n ma mb ha hb conflict
    simp only [releaseAll] at ha hb
    have haOld : s.held a n = some ma := by split at ha <;> simp_all
    have hbOld : s.held b n = some mb := by split at hb <;> simp_all
    exact hs a b n ma mb haOld hbOld conflict

theorem step_ordered (ho : Ordered s) (step : Step s s') : Ordered s' := by
  cases step with
  | request t r idle order =>
    intro u q pending n m held
    simp only [setWait] at pending held
    by_cases h : u = t
    · subst u
      simp only [ite_true, Option.some.injEq] at pending
      subst q
      exact order n m held
    · simp only [h, ite_false] at pending
      exact ho u q pending n m held
  | grant t r pending available =>
    intro u q wait n m held
    simp only [setWait, setHeld] at wait held
    by_cases h : u = t
    · simp [h] at wait
    · simp only [h, ite_false, false_and] at wait held
      exact ho u q wait n m held
  | release t n =>
    intro u q wait k m held
    simp only [setHeld] at wait held
    split at held <;> simp_all
    exact ho u q wait k m held
  | tryUpgrade t n idle reader sole =>
    intro u q wait k m held
    simp only [setHeld] at wait held
    by_cases h : u = t
    · subst u
      rw [idle] at wait
      contradiction
    · simp only [h, false_and, ite_false] at held
      exact ho u q wait k m held
  | retry t =>
    intro u q wait k m held
    simp only [releaseAll] at wait held
    by_cases h : u = t
    · simp [h] at wait
    · simp only [h, ite_false] at wait held
      exact ho u q wait k m held

theorem reachable_safe (hr : Reachable s) : Safe s := by
  induction hr with
  | initial => exact empty_safe
  | next _ step ih => exact step_safe ih step

theorem reachable_ordered (hr : Reachable s) : Ordered s := by
  induction hr with
  | initial => exact empty_ordered
  | next _ step ih => exact step_ordered ih step

/- Access discipline: every data access stays inside its node's lock scope.
Sorting, sortedness-bit changes, and pointer changes count as writes. Read
permission alone does not authorize sorting, even when the API is a query. -/
def MayRead (s : State) (t : Thread) (n : Node) : Prop :=
  ∃ m, s.held t n = some m

def MayWrite (s : State) (t : Thread) (n : Node) : Prop :=
  s.held t n = some .write

theorem no_reader_writer_race (hr : Reachable s) (different : reader ≠ writer)
    (read : MayRead s reader n) (write : MayWrite s writer n) : False := by
  obtain ⟨m, hm⟩ := read
  exact different (reachable_safe hr reader writer n m .write hm write (Or.inr rfl))

theorem no_writer_writer_race (hr : Reachable s) (different : a ≠ b)
    (wa : MayWrite s a n) (wb : MayWrite s b n) : False := by
  exact no_reader_writer_race hr different ⟨.write, wa⟩ wb

/- A waits for B when B holds an incompatible lock A has requested.
Queue-priority dependencies are NOT modeled: the lock primitive must not add
backward dependencies through an upgrade queue, recursive locking, or callbacks.
-/
def WaitsFor (s : State) (a b : Thread) : Prop :=
  ∃ r m, s.waiting a = some r ∧ s.held b r.node = some m ∧ Conflicts r.mode m

theorem wait_rank_increases (ho : Ordered s) (edge : WaitsFor s a b)
    (wa : s.waiting a = some ra) (wb : s.waiting b = some rb) :
    ra.node < rb.node := by
  obtain ⟨r, m, wr, held, _⟩ := edge
  have eq : r = ra := Option.some.inj (wr.symm.trans wa)
  subst r
  exact ho b rb wb ra.node m held

inductive WaitPath (s : State) : Thread → Thread → Prop where
  | edge : WaitsFor s a b → WaitPath s a b
  | cons : WaitsFor s a b → WaitPath s b c → WaitPath s a c

theorem path_rank_increases (ho : Ordered s) (path : WaitPath s a b)
    (wa : s.waiting a = some ra) (wb : s.waiting b = some rb) :
    ra.node < rb.node := by
  induction path generalizing ra rb with
  | edge e => exact wait_rank_increases ho e wa wb
  | @cons a b c edge path ih =>
    obtain ⟨r, m, pending, held, conflict⟩ := edge
    have middle : ∃ q, s.waiting b = some q := by
      cases path with
      | edge e => obtain ⟨q, _, h, _, _⟩ := e; exact ⟨q, h⟩
      | cons e _ => obtain ⟨q, _, h, _, _⟩ := e; exact ⟨q, h⟩
    obtain ⟨q, wq⟩ := middle
    exact Nat.lt_trans (wait_rank_increases ho ⟨r, m, pending, held, conflict⟩ wa wq)
      (ih wq wb)

theorem no_wait_cycle (hr : Reachable s) : ¬ WaitPath s t t := by
  intro path
  have pending : ∃ r, s.waiting t = some r := by
    cases path with
    | edge e => obtain ⟨r, _, h, _, _⟩ := e; exact ⟨r, h⟩
    | cons e _ => obtain ⟨r, _, h, _, _⟩ := e; exact ⟨r, h⟩
  obtain ⟨r, wr⟩ := pending
  exact Nat.lt_irrefl r.node (path_rank_increases (reachable_ordered hr) path wr wr)

/- Absence of cycles is strengthened to absence of a finite, nonempty closed
set of blocked threads. This rules out partial lock deadlocks too, even when
unrelated threads are still running. It does not assert scheduling fairness,
starvation freedom, or progress if a lock holder crashes or loops forever. -/
private theorem finite_max (f : Thread → Nat) (ts : List Thread) (hne : ts ≠ []) :
    ∃ t ∈ ts, ∀ u ∈ ts, f u ≤ f t := by
  induction ts with
  | nil => contradiction
  | cons a tail ih =>
    by_cases ht : tail = []
    · subst tail
      exact ⟨a, by simp, by simp⟩
    · obtain ⟨b, hb, hmax⟩ := ih ht
      by_cases hab : f a ≤ f b
      · refine ⟨b, by simp [hb], ?_⟩
        intro u hu
        rcases List.mem_cons.mp hu with rfl | hu
        · exact hab
        · exact hmax u hu
      · refine ⟨a, by simp, ?_⟩
        intro u hu
        rcases List.mem_cons.mp hu with rfl | hu
        · exact Nat.le_refl _
        · have := hmax u hu
          omega

def waitRank (s : State) (t : Thread) : Nat :=
  match s.waiting t with
  | none => 0
  | some r => r.node

def Deadlocked (s : State) (ts : List Thread) : Prop :=
  ts ≠ [] ∧ ∀ t ∈ ts, ∃ u ∈ ts, WaitsFor s t u

theorem no_finite_deadlock (hr : Reachable s) (ts : List Thread) :
    ¬ Deadlocked s ts := by
  rintro ⟨hne, closed⟩
  obtain ⟨t, ht, hmax⟩ := finite_max (waitRank s) ts hne
  obtain ⟨u, hu, edge⟩ := closed t ht
  obtain ⟨v, _, nextEdge⟩ := closed u hu
  obtain ⟨ra, ma, wa, ha, ca⟩ := edge
  obtain ⟨rb, mb, wb, hb, cb⟩ := nextEdge
  have inc : ra.node < rb.node := wait_rank_increases (reachable_ordered hr) ⟨ra, ma, wa, ha, ca⟩ wa wb
  have bound := hmax u hu
  simp only [waitRank, wa, wb] at bound
  exact Nat.not_lt_of_ge bound inc

/- Concrete executions and negative controls. These are witnesses, not the
universal proof above. `takeLock` is exactly a request followed by a grant. -/
def takeLock (s : State) (t : Thread) (r : Request) : State :=
  setWait (setHeld (setWait s t (some r)) t r.node (some r.mode)) t none

theorem reachable_take (hr : Reachable s) (idle : s.waiting t = none)
    (order : ∀ n m, s.held t n = some m → n < r.node)
    (available : Compatible s r) : Reachable (takeLock s t r) := by
  apply Reachable.next (Reachable.next hr (Step.request s t r idle order))
  exact Step.grant _ t r (by simp [setWait]) available

def twoReaders : State :=
  takeLock (takeLock empty 0 ⟨1, .read⟩) 1 ⟨1, .read⟩

theorem twoReaders_reachable : Reachable twoReaders := by
  apply reachable_take
  · apply reachable_take Reachable.initial
    · rfl
    · simp [empty]
    · simp [Compatible, empty]
  · simp [takeLock, setWait, setHeld, empty]
  · simp [takeLock, setWait, setHeld, empty]
  · intro u m hm
    simp only [takeLock, setWait, setHeld, empty] at hm
    split at hm
    · simp_all [Conflicts]
      subst m
      decide
    · contradiction

/- NEGATIVE CONTROL 1: change the protocol to allow blocking upgrades that
retain R. Two readers then form a deadlock, even though lock mutual exclusion
itself still holds. This is NOT the release-before-write scan rule in §5. -/
inductive BadStep : State → State → Prop where
  | normal : Step s s' → BadStep s s'
  | blockingUpgrade (s : State) (t : Thread) (n : Node)
      (idle : s.waiting t = none) (reader : s.held t n = some .read) :
      BadStep s (setWait s t (some ⟨n, .write⟩))

inductive BadReachable : State → Prop where
  | normal : Reachable s → BadReachable s
  | next : BadReachable s → BadStep s s' → BadReachable s'

def upgradeDeadlock : State :=
  setWait (setWait twoReaders 0 (some ⟨1, .write⟩)) 1 (some ⟨1, .write⟩)

theorem upgradeDeadlock_reachable : BadReachable upgradeDeadlock := by
  apply BadReachable.next
  · apply BadReachable.next (BadReachable.normal twoReaders_reachable)
    exact BadStep.blockingUpgrade _ 0 1
      (by simp [twoReaders, takeLock, setWait, setHeld, empty])
      (by simp [twoReaders, takeLock, setWait, setHeld, empty])
  · exact BadStep.blockingUpgrade _ 1 1
      (by simp [setWait, twoReaders, takeLock])
      (by simp [setWait, twoReaders, takeLock, setHeld, empty])

theorem blocking_upgrade_deadlocks : Deadlocked upgradeDeadlock [0, 1] := by
  refine ⟨by decide, ?_⟩
  intro t ht
  simp only [List.mem_cons, List.not_mem_nil, or_false] at ht
  rcases ht with rfl | rfl
  · refine ⟨1, by simp, ⟨⟨1, .write⟩, .read, ?_, ?_, Or.inl rfl⟩⟩
    · rfl
    · rfl
  · refine ⟨0, by simp, ⟨⟨1, .write⟩, .read, ?_, ?_, Or.inl rfl⟩⟩
    · rfl
    · rfl

theorem blocking_upgrade_still_mutex : Safe upgradeDeadlock := by
  have h := reachable_safe twoReaders_reachable
  exact h

/- A writer waiting for a leaf while holding its parent's R lock is harmless
only if the leaf owner's parent upgrade is nonblocking. The real rule fails
the try-upgrade, releases its locks, and permits the waiting writer to run. -/
def parentLeaf : State :=
  takeLock (takeLock (takeLock empty 0 ⟨0, .read⟩) 0 ⟨1, .write⟩) 1 ⟨0, .read⟩

theorem parentLeaf_reachable : Reachable parentLeaf := by
  apply reachable_take
  · apply reachable_take
    · apply reachable_take Reachable.initial
      · rfl
      · simp [empty]
      · simp [Compatible, empty]
    · simp [takeLock, setWait, setHeld, empty]
    · intro n m hm
      simp only [takeLock, setHeld, setWait, empty] at hm
      split at hm
      · rename_i h
        simp_all
      · contradiction
    · simp [Compatible, takeLock, setWait, setHeld, empty]
  · simp [takeLock, setWait, setHeld, empty]
  · simp [takeLock, setWait, setHeld, empty]
  · intro u m hm
    simp only [takeLock, setWait, setHeld, empty] at hm
    split at hm
    · rename_i h
      simp_all
    · split at hm
      · simp_all [Conflicts]
        subst m
        decide
      · contradiction

def parentLeafWait : State := setWait parentLeaf 1 (some ⟨1, .write⟩)

theorem parentLeafWait_reachable : Reachable parentLeafWait := by
  apply Reachable.next parentLeaf_reachable
  apply Step.request
  · simp [parentLeaf, takeLock, setWait]
  · intro n m hm
    simp only [parentLeaf, takeLock, setHeld, setWait] at hm
    split at hm
    · rename_i h
      simp_all
    · simp [empty] at hm

theorem parent_upgrade_must_fail :
    ¬ (∀ u m, parentLeafWait.held u 0 = some m → u = 0) := by
  intro sole
  have := sole 1 .read (by rfl)
  contradiction

theorem parent_retry_unblocks_leaf :
    Step parentLeafWait (releaseAll parentLeafWait 0) ∧
    Compatible (releaseAll parentLeafWait 0) ⟨1, .write⟩ := by
  refine ⟨Step.retry _ _, ?_⟩
  intro u m hm
  simp only [releaseAll, parentLeafWait, parentLeaf, setWait, takeLock, setHeld,
    empty] at hm
  split at hm
  · contradiction
  · split at hm
    · rename_i h
      simp_all
    · split at hm
      · rename_i h
        simp_all
      · split at hm <;> simp_all

/- NEGATIVE CONTROL 2: making the parent upgrade blocking creates precisely
the parent/leaf inversion the optimistic restart is intended to avoid.

Thread 0: holds R(parent), W(leaf); waits for W(parent).
Thread 1: holds R(parent);         waits for W(leaf).
-/
def parentUpgradeDeadlock : State :=
  setWait parentLeafWait 0 (some ⟨0, .write⟩)

theorem parentUpgradeDeadlock_reachable : BadReachable parentUpgradeDeadlock := by
  apply BadReachable.next (BadReachable.normal parentLeafWait_reachable)
  exact BadStep.blockingUpgrade _ 0 0 (by rfl) (by rfl)

theorem blocking_parent_upgrade_deadlocks : Deadlocked parentUpgradeDeadlock [0, 1] := by
  refine ⟨by decide, ?_⟩
  intro t ht
  simp only [List.mem_cons, List.not_mem_nil, or_false] at ht
  rcases ht with rfl | rfl
  · exact ⟨1, by simp, ⟨⟨0, .write⟩, .read, rfl, rfl, Or.inl rfl⟩⟩
  · exact ⟨0, by simp, ⟨⟨1, .write⟩, .write, rfl, rfl, Or.inl rfl⟩⟩

/- THE UNLOCK/RELOCK GAP: a legal split can invalidate a scan's old routing
decision or cached successor while it owns no leaf lock. This witness does NOT
show a data race, a deadlock, or necessarily an incorrect scan in the paper:
following the *current* successor after reacquisition reaches the moved key.
It disproves the assumption that reacquiring the same node restores the same
contents/topology. A proof of query correctness must address this explicitly.

Node 0 is parent, node 1 is old leaf, node 2 is the new right leaf. Thread 0
scans; thread 1 splits. The writer holds all three W locks during the split.
The new node is treated as preallocated, private, and initially empty. The
parent separator update is abstracted by requiring its W lock.
-/
structure GapState where
  locks : State
  leftKeys : List Nat
  rightKeys : List Nat
  nextLeaf : Option Node
  savedNext : Option Node

def gapInitial : GapState := ⟨empty, [10, 20], [], none, none⟩

inductive GapStep : GapState → GapState → Prop where
  | lock (g : GapState) (step : Step g.locks s') :
      GapStep g { g with locks := s' }
  | capture (g : GapState) (read : MayRead g.locks 0 1) :
      GapStep g { g with savedNext := g.nextLeaf }
  | split (g : GapState)
      (parent : MayWrite g.locks 1 0)
      (left : MayWrite g.locks 1 1)
      (right : MayWrite g.locks 1 2)
      (contents : g.leftKeys = [10, 20])
      (fresh : g.rightKeys = [] ∧ g.nextLeaf = none) :
      GapStep g { g with leftKeys := [10], rightKeys := [20], nextLeaf := some 2 }

inductive GapReachable : GapState → Prop where
  | initial : GapReachable gapInitial
  | next : GapReachable g → GapStep g g' → GapReachable g'

theorem gap_locks_reachable (h : GapReachable g) : Reachable g.locks := by
  induction h with
  | initial => exact Reachable.initial
  | next _ step ih =>
    cases step with
    | lock _ step => exact Reachable.next ih step
    | capture => exact ih
    | split => exact ih

def gapTake (g : GapState) (t : Thread) (r : Request) : GapState :=
  { g with locks := takeLock g.locks t r }

theorem gap_take (hg : GapReachable g) (idle : g.locks.waiting t = none)
    (order : ∀ n m, g.locks.held t n = some m → n < r.node)
    (available : Compatible g.locks r) : GapReachable (gapTake g t r) := by
  apply GapReachable.next
  · exact GapReachable.next hg (GapStep.lock g (Step.request _ t r idle order))
  · exact GapStep.lock _ (Step.grant _ t r (by simp [setWait]) available)

def g1 := gapTake gapInitial 0 ⟨1, .read⟩
def g2 : GapState := { g1 with savedNext := g1.nextLeaf }
def g3 : GapState := { g2 with locks := setHeld g2.locks 0 1 none }
def g4 := gapTake g3 1 ⟨0, .write⟩
def g5 := gapTake g4 1 ⟨1, .write⟩
def g6 := gapTake g5 1 ⟨2, .write⟩
def g7 : GapState := { g6 with leftKeys := [10], rightKeys := [20], nextLeaf := some 2 }
def g8 : GapState := { g7 with locks := releaseAll g7.locks 1 }
def g9 := gapTake g8 0 ⟨1, .write⟩

/- `grind` below discharges finite concrete lock guards by symbolic reduction;
the universal soundness proof does not depend on these examples. -/
theorem gap_execution : GapReachable g9 := by
  have h1 : GapReachable g1 := by
    apply gap_take GapReachable.initial
    · rfl
    · simp [gapInitial, empty]
    · simp [Compatible, gapInitial, empty]
  have h2 : GapReachable g2 :=
    GapReachable.next h1 (GapStep.capture _ ⟨.read, rfl⟩)
  have h3 : GapReachable g3 := GapReachable.next h2 (GapStep.lock _ (Step.release _ 0 1))
  have h4 : GapReachable g4 := by
    apply gap_take h3
    · rfl
    · simp [g3, g2, g1, gapTake, takeLock, setHeld, setWait, gapInitial, empty]
    · intro u m hm
      simp [g3, g2, g1, gapTake, takeLock, setHeld, setWait, gapInitial, empty] at hm
  have h5 : GapReachable g5 := by
    apply gap_take h4
    · rfl
    · intro n m hm
      simp [g4, g3, g2, g1, gapTake, takeLock, setHeld, setWait, gapInitial, empty] at hm
      grind
    · intro u m hm
      simp [g4, g3, g2, g1, gapTake, takeLock, setHeld, setWait, gapInitial, empty] at hm
      grind
  have h6 : GapReachable g6 := by
    apply gap_take h5
    · rfl
    · intro n m hm
      simp [g5, g4, g3, g2, g1, gapTake, takeLock, setHeld, setWait, gapInitial, empty] at hm
      grind
    · intro u m hm
      simp [g5, g4, g3, g2, g1, gapTake, takeLock, setHeld, setWait, gapInitial, empty] at hm
  have h7 : GapReachable g7 :=
    GapReachable.next h6 (GapStep.split _ rfl rfl rfl rfl ⟨rfl, rfl⟩)
  have h8 : GapReachable g8 := GapReachable.next h7 (GapStep.lock _ (Step.retry _ 1))
  apply gap_take h8
  · rfl
  · intro n m hm
    simp [g8, g7, g6, g5, g4, g3, g2, g1, gapTake, takeLock, setHeld, setWait,
      releaseAll, gapInitial, empty] at hm
    grind
  · intro u m hm
    simp [g8, g7, g6, g5, g4, g3, g2, g1, gapTake, takeLock, setHeld, setWait,
      releaseAll, gapInitial, empty] at hm
    grind

theorem split_during_scan_gap :
    GapReachable g9 ∧ MayWrite g9.locks 0 1 ∧
    20 ∈ g2.leftKeys ∧ 20 ∉ g9.leftKeys ∧ 20 ∈ g9.rightKeys ∧
    g9.savedNext = none ∧ g9.nextLeaf = some 2 := by
  exact ⟨gap_execution, rfl, by decide, by decide, by decide, rfl, rfl⟩

theorem gap_still_has_no_deadlock (ts : List Thread) : ¬ Deadlocked g9.locks ts :=
  no_finite_deadlock (gap_locks_reachable gap_execution) ts

/- Original lock-only assessment (extended below)

1. PROVED: the explicit ordered lock machine preserves mutual exclusion and
   has no wait cycle / finite closed lock deadlock. Atomic try-upgrade succeeds
   only as sole reader; failure releases locks and restarts. Scan conversion
   releases R before requesting W. These rules agree with §§2.1 and 5.
2. PROVED HAZARDS OF MODIFICATIONS: retaining R in a blocking upgrade deadlocks,
   including the concrete parent/leaf scenario. Mutual exclusion alone cannot
   prove deadlock freedom. These bad transitions are not attributed to §5.
3. PROVED GAP HAZARD: a legal, exclusively locked split moves a key and changes
   the successor between a scan's R unlock and W acquisition. Using cached
   topology after this gap is unjustified. This witness alone does not refute
   the paper's scan: rereading the current leaf and following its current next
   pointer handles this example. This original model does not prove snapshots;
   the extensions below refute them for hand-over-hand scans and prove a repair.
4. CONDITIONAL MAPPING TO A REAL TREE: all blocking acquisitions must respect
   one strict order across locks held by the same thread. A fixed rank models
   a fixed finite allocation; dynamic splits/root replacement must preserve
   an appropriate order, not reuse the natural numbers here as allocation IDs.
   Split propagation must retain ancestors or restart; reacquiring an ancestor
   while holding a child is excluded. The paper's "at most two locks" sentence
   does not specify the full pessimistic split propagation protocol.
5. OTHER OBLIGATIONS: protect root/parent/sibling pointers, sortedness bits and
   counters; recompute or validate pointers/cursors across unlocks; keep nodes
   alive across the gap; synchronize publication and reclamation; forbid or
   handle callback reentry. The paper omits deletion details, so merges,
   reclamation, and reverse sibling acquisition are not verified here.
6. PRIMITIVE ASSUMPTIONS: atomic RW-lock transitions with the usual memory
   visibility guarantees, nonblocking atomic try-upgrade, no extra queue-order
   wait dependencies, nonrecursive acquisitions, and every data access under
   its node lock. Scheduler/lock fairness and starvation freedom are separate.

Thus the described lock-order/upgrade discipline is sound in this abstraction;
the prose alone is insufficient for an unconditional proof of the full tree.
-/

-- Audit proof dependencies. Standard logical axioms may appear; sorryAx must not.
#print axioms reachable_safe
#print axioms no_reader_writer_race
#print axioms no_writer_writer_race
#print axioms no_wait_cycle
#print axioms no_finite_deadlock
#print axioms blocking_upgrade_deadlocks
#print axioms blocking_parent_upgrade_deadlocks
#print axioms split_during_scan_gap

/-!
## Standard Go primitives: no upgrade operation

Go sources checked against the installed go1.26.4 and official documentation:
https://pkg.go.dev/sync#RWMutex
https://go.dev/src/internal/sync/mutex.go

RWMutex has no atomic R-to-W upgrade. TryLock is not an upgrade while holding R.
The following transition system has ONLY blocking lock requests, completed
acquisitions, and matching unlocks. Mode.write alone also models sync.Mutex.
There is no CAS, spin loop, atomic upgrade, or atomic "release all" operation.

Implementation policy:
* Find: RLock coupling down the tree.
* Insert: optimistic RLock descent, leaf Lock. If an ancestor must change,
  release every held node lock individually, then restart at the root with
  Lock acquired top-down. Keep the ancestors needed for split propagation.
  Do not reacquire a parent while holding its child. Alternatively use this
  pessimistic descent from the start; it avoids retry starvation.
* Scan: take each leaf's Lock directly if sorting might be needed, or RUnlock
  before Lock and rebuild its cursor. Never upgrade a held RLock.
* New allocation and root publication still require an implementation-level
  order/lifetime proof. This is a protocol change, not an implementation of
  the paper's atomic parent try-upgrade using RWMutex.

Request/grant separation abstracts blocking Lock/RLock. Grant order is left
unspecified. Go's writer preference can remove grants from this abstraction;
it does not weaken mutual exclusion. Internal queue progress is a property
required of the lock primitive, not a theorem about the Go runtime here.
-/
namespace GoPrimitives

inductive Step : State → State → Prop where
  | request (s : State) (t : Thread) (r : Request)
      (idle : s.waiting t = none)
      (order : ∀ n m, s.held t n = some m → n < r.node) :
      Step s (setWait s t (some r))
  | grant (s : State) (t : Thread) (r : Request)
      (pending : s.waiting t = some r) (available : Compatible s r) :
      Step s (setWait (setHeld s t r.node (some r.mode)) t none)
  | unlock (s : State) (t : Thread) (n : Node) (m : Mode)
      (owns : s.held t n = some m) (idle : s.waiting t = none) :
      Step s (setHeld s t n none)

theorem step_refines (h : Step s s') : BPTreeLocks.Step s s' := by
  cases h with
  | request t r idle order => exact BPTreeLocks.Step.request _ t r idle order
  | grant t r pending available => exact BPTreeLocks.Step.grant _ t r pending available
  | unlock t n m owns idle => exact BPTreeLocks.Step.release _ t n

inductive Reachable : State → Prop where
  | initial : Reachable empty
  | next : Reachable s → Step s s' → Reachable s'

theorem reachable_refines (h : Reachable s) : BPTreeLocks.Reachable s := by
  induction h with
  | initial => exact BPTreeLocks.Reachable.initial
  | next _ step ih => exact BPTreeLocks.Reachable.next ih (step_refines step)

theorem race_free (h : Reachable s) : Safe s := reachable_safe (reachable_refines h)

theorem deadlock_free (h : Reachable s) (ts : List Thread) : ¬ Deadlocked s ts :=
  no_finite_deadlock (reachable_refines h) ts

/- RWMutex's pending writers can block new readers even without a conflicting
CURRENT holder. Account for these queue dependencies explicitly. Within one
primitive, a waiter may depend on an earlier admission stage of that same
primitive. `priority` increases toward that stage; it need not be arrival FIFO.
The primitive must supply an acyclic local admission order. Go's internal
queues are not reimplemented here. Crucially, all their waiters have already
passed our no-recursion/ascending-acquisition guard.

A holder dependency strictly increases the requested NODE rank. A local queue
dependency keeps the node rank equal and increases admission priority. Thus
neither writer preference nor orderly mutex queues create a new inter-node
deadlock. Fairness is still a separate issue.
-/
def QueueDependency (s : State) (priority : Thread → Nat) (a b : Thread) : Prop :=
  ∃ ra rb, s.waiting a = some ra ∧ s.waiting b = some rb ∧
    ra.node = rb.node ∧ priority a < priority b

def WaitsWithQueues (s : State) (priority : Thread → Nat) (a b : Thread) : Prop :=
  WaitsFor s a b ∨ QueueDependency s priority a b

def DeadlockedWithQueues (s : State) (priority : Thread → Nat) (ts : List Thread) : Prop :=
  ts ≠ [] ∧ ∀ t ∈ ts, ∃ u ∈ ts, WaitsWithQueues s priority t u

private theorem dependency_pending (edge : WaitsWithQueues s priority a b) :
    ∃ r, s.waiting a = some r := by
  rcases edge with holder | queued
  · obtain ⟨r, _, pending, _, _⟩ := holder
    exact ⟨r, pending⟩
  · obtain ⟨ra, _, pending, _, _, _⟩ := queued
    exact ⟨ra, pending⟩

def requestWeight (s : State) (priority : Thread → Nat) (bound : Nat) (t : Thread) : Nat :=
  waitRank s t * (bound + 1) + priority t

private theorem dependency_increases (ordered : Ordered s)
    (edge : WaitsWithQueues s priority a b)
    (wa : s.waiting a = some ra) (wb : s.waiting b = some rb)
    (bounded : priority a ≤ bound) :
    requestWeight s priority bound a < requestWeight s priority bound b := by
  simp only [requestWeight, waitRank, wa, wb]
  rcases edge with holder | queued
  · have inc := wait_rank_increases ordered holder wa wb
    have product := Nat.mul_le_mul_right (bound + 1) (Nat.succ_le_of_lt inc)
    simp only [Nat.succ_mul] at product
    omega
  · obtain ⟨qa, qb, ha, hb, same, inc⟩ := queued
    have ea : qa = ra := Option.some.inj (ha.symm.trans wa)
    have eb : qb = rb := Option.some.inj (hb.symm.trans wb)
    subst qa qb
    rw [same]
    exact Nat.add_lt_add_left inc _

theorem deadlock_free_with_queues (h : Reachable s) (priority : Thread → Nat)
    (ts : List Thread) : ¬ DeadlockedWithQueues s priority ts := by
  rintro ⟨nonempty, closed⟩
  obtain ⟨p, _, priorities⟩ := finite_max priority ts nonempty
  obtain ⟨t, ht, maximal⟩ := finite_max (requestWeight s priority (priority p)) ts nonempty
  obtain ⟨u, hu, edge⟩ := closed t ht
  obtain ⟨v, _, next⟩ := closed u hu
  obtain ⟨ra, wa⟩ := dependency_pending edge
  obtain ⟨rb, wb⟩ := dependency_pending next
  have inc := dependency_increases (reachable_ordered (reachable_refines h))
    edge wa wb (priorities t ht)
  exact Nat.not_lt_of_ge (maximal u hu) inc

end GoPrimitives

/-!
## Hand-over-hand scans do NOT guarantee a snapshot

Three leaves are important: adjacent lock overlap alone can protect a two-leaf
example. Here the scanner correctly couples A->B->C without any upgrade gap.
Initially the three values are (0,0,0). The scan reads A=0, locks B, unlocks A.
A writer sets A=1, then sets C=1 in two separate completed point updates.
The scanner then reads B=0, locks C, unlocks B, and reads C=1.
The returned (0,0,1) never existed: the only database states are (0,0,0),
(1,0,0), and (1,0,1). Thus it is not a linearizable snapshot scan.

The following executable transition system checks EVERY lock and data access
in that history. Leaves have fixed keys; only their associated values change.
No splits, deletion, dirty blocks, memory reclamation, or API callbacks are
needed. Descent is abstracted away on this fixed topology; each writer can
perform an ordinary descent to its leaf before its lock acquisition.
-/
namespace SnapshotCounterexample

abbrev T := Fin 2
abbrev L := Fin 3

structure Machine where
  held : T → L → Option Mode
  value : L → Nat
  observed : L → Option Nat

def initial : Machine := ⟨fun _ _ => none, fun _ => 0, fun _ => none⟩

inductive Action where
  | lock (t : T) (n : L) (mode : Mode)
  | unlock (t : T) (n : L)
  | read (n : L)
  | write (n : L) (value : Nat)

def exec (s : Machine) : Action → Option Machine
  | .lock t n mode =>
    if (∀ k : L, s.held t k ≠ none → k.val < n.val) ∧
       (∀ u : T, ∀ m : Mode, s.held u n = some m →
         ¬ (mode = .write ∨ m = .write)) then
      some { s with held := fun u k => if u = t ∧ k = n then some mode else s.held u k }
    else none
  | .unlock t n =>
    if s.held t n ≠ none then
      some { s with held := fun u k => if u = t ∧ k = n then none else s.held u k }
    else none
  | .read n =>
    if s.held 0 n ≠ none then
      some { s with observed := fun k => if k = n then some (s.value n) else s.observed k }
    else none
  | .write n v =>
    if s.held 1 n = some .write then
      some { s with value := fun k => if k = n then v else s.value k }
    else none

def view (s : Machine) : List Nat := [s.value 0, s.value 1, s.value 2]

-- Record every state, including the initial and final states of the scan.
def run (s : Machine) : List Action → Option (Machine × List (List Nat))
  | [] => some (s, [view s])
  | a :: rest => do
    let next ← exec s a
    let (final, history) ← run next rest
    pure (final, view s :: history)

def schedule : List Action := [
  .lock 0 0 .read, .read 0,
  .lock 0 1 .read, .unlock 0 0,
  .lock 1 0 .write, .write 0 1, .unlock 1 0,
  .lock 1 2 .write, .write 2 1, .unlock 1 2,
  .read 1, .lock 0 2 .read, .unlock 0 1, .read 2, .unlock 0 2]

def result := run initial schedule
def finalState : Machine := (result.getD (initial, [])).1
def history : List (List Nat) := (result.getD (initial, [])).2

theorem legal_execution : result = some (finalState, history) := by rfl

theorem observed_values :
    finalState.observed 0 = some 0 ∧ finalState.observed 1 = some 0 ∧
    finalState.observed 2 = some 1 := by decide

theorem no_snapshot_in_entire_history : ∀ values ∈ history, values ≠ [0, 0, 1] := by
  decide

theorem hand_over_hand_not_snapshot :
    result = some (finalState, history) ∧
    [finalState.observed 0, finalState.observed 1, finalState.observed 2] =
      [some 0, some 0, some 1] ∧
    ¬ ∃ values ∈ history, values = [0, 0, 1] := by
  refine ⟨legal_execution, by decide, ?_⟩
  rintro ⟨values, member, eq⟩
  exact no_snapshot_in_entire_history values member eq

end SnapshotCounterexample

/-!
## A snapshot repair using only sync.RWMutex and ordinary node locks

Add an admission gate of rank 0; all tree nodes have higher ranks.

  Point operation:
      gate.RLock()
      ... acquire/release ordered node locks; perform the whole operation ...
      gate.RUnlock()  // AFTER releasing all node locks

  Snapshot range operation:
      gate.Lock()     // acquire directly, never while holding gate.RLock()
      ... traverse, sort, copy the complete result ...
      gate.Unlock()   // AFTER releasing all node locks
      ... deliver copied results / invoke user callbacks ...

Different point operations can still run concurrently using the node locks.
A snapshot excludes point operations and other snapshots for its full span.
This is an added protocol, NOT a claim about the paper's throughput. Replacing
the whole scheme with one sync.Mutex around each operation is an even simpler
fully serialized implementation of the same isolation idea.

All logical mutations (including inserts, deletes, and root replacement) must
be inside the point-operation gate RLock interval. Sorting under a scan's gate
Lock changes only physical layout; its preservation of the logical map is a
separate sequential data-structure obligation. Do not call user callbacks
under the gate if they may reenter the tree. Copies must not expose mutable
internal storage after unlocking.

The proof below freezes the entire logical map while the snapshot holds the
gate, and proves every emitted observation agrees with the map at acquisition.
It works for arbitrary keys, present/absent values, mutations, and scan lengths.
The proof does not establish that a concrete tree traversal enumerates every
key in a requested interval: that is a separate sequential-correctness proof.
-/
namespace SnapshotGate

abbrev Store := Nat → Option Nat

structure Machine where
  locks : State
  data : Store
  output : List (Nat × Option Nat)

def initial (data : Store) : Machine := ⟨empty, data, []⟩

inductive Step (scanner : Thread) : Machine → Machine → Prop where
  | lock (g : Machine) (h : GoPrimitives.Step g.locks locks') :
      Step scanner g { g with locks := locks' }
  | mutate (g : Machine) (t : Thread) (node : Node) (newData : Store)
      (gate : g.locks.held t 0 = some .read)
      (nodeLock : MayWrite g.locks t node) (treeNode : 0 < node) :
      Step scanner g { g with data := newData }
  | sample (g : Machine) (key : Nat)
      (gate : MayWrite g.locks scanner 0) :
      Step scanner g { g with output := g.output ++ [(key, g.data key)] }

inductive Reachable (scanner : Thread) (start : Store) : Machine → Prop where
  | initial : Reachable scanner start (initial start)
  | next : Reachable scanner start g → Step scanner g g' → Reachable scanner start g'

theorem locks_reachable (h : Reachable scanner start g) : GoPrimitives.Reachable g.locks := by
  induction h with
  | initial => exact GoPrimitives.Reachable.initial
  | next _ step ih =>
    cases step with
    | lock _ h => exact GoPrimitives.Reachable.next ih h
    | mutate => exact ih
    | sample => exact ih

theorem gate_excludes_mutators (safe : Safe s) (held : MayWrite s scanner 0)
    (reader : s.held t 0 = some .read) : False := by
  have eq : t = scanner := safe t scanner 0 .read .write reader held (Or.inr rfl)
  subst t
  rw [held] at reader
  contradiction

theorem step_safe (safe : Safe g.locks) (h : Step scanner g g') : Safe g'.locks := by
  cases h with
  | lock _ h => exact BPTreeLocks.step_safe safe (GoPrimitives.step_refines h)
  | mutate => exact safe
  | sample => exact safe

theorem step_freezes_data (safe : Safe g.locks) (held : MayWrite g.locks scanner 0)
    (h : Step scanner g g') : g'.data = g.data := by
  cases h with
  | lock => rfl
  | mutate _ t node newData gate _ _ => exact False.elim (gate_excludes_mutators safe held gate)
  | sample => rfl

-- Every state in this segment retains the gate continuously, including its end.
inductive Segment (scanner : Thread) : Machine → Machine → Prop where
  | nil (g : Machine) (held : MayWrite g.locks scanner 0) : Segment scanner g g
  | cons (held : MayWrite g.locks scanner 0) (step : Step scanner g g')
      (rest : Segment scanner g' finish) : Segment scanner g finish

theorem whole_map_frozen (safe : Safe g.locks) (h : Segment scanner g finish) :
    finish.data = g.data := by
  induction h with
  | nil => rfl
  | cons held step rest ih =>
    exact (ih (step_safe safe step)).trans (step_freezes_data safe held step)

def OutputMatches (snapshot : Store) (out : List (Nat × Option Nat)) : Prop :=
  ∀ pair ∈ out, pair.2 = snapshot pair.1

theorem step_output_matches (safe : Safe g.locks) (held : MayWrite g.locks scanner 0)
    (h : Step scanner g g') (frozen : g.data = snapshot)
    (before : OutputMatches snapshot g.output) : OutputMatches snapshot g'.output := by
  cases h with
  | lock => exact before
  | mutate _ t node newData gate _ _ => exact False.elim (gate_excludes_mutators safe held gate)
  | sample g key gate =>
    intro pair member
    simp only [List.mem_append, List.mem_singleton] at member
    rcases member with old | rfl
    · exact before pair old
    · exact congrFun frozen key

theorem segment_output_matches (safe : Safe g.locks) (h : Segment scanner g finish)
    (frozen : g.data = snapshot) (before : OutputMatches snapshot g.output) :
    OutputMatches snapshot finish.output := by
  induction h with
  | nil => exact before
  | cons held step rest ih =>
    apply ih (step_safe safe step)
    · exact (step_freezes_data safe held step).trans frozen
    · exact step_output_matches safe held step frozen before

theorem snapshot_consistent (reachable : Reachable scanner start g)
    (h : Segment scanner g finish) (freshOutput : g.output = []) :
    finish.data = g.data ∧ OutputMatches g.data finish.output := by
  have safe := GoPrimitives.race_free (locks_reachable reachable)
  refine ⟨whole_map_frozen safe h, segment_output_matches safe h rfl ?_⟩
  simp [OutputMatches, freshOutput]

theorem gate_no_lock_deadlock (reachable : Reachable scanner start g) (ts : List Thread) :
    ¬ Deadlocked g.locks ts := GoPrimitives.deadlock_free (locks_reachable reachable) ts

theorem gate_no_queue_deadlock (reachable : Reachable scanner start g)
    (priority : Thread → Nat) (ts : List Thread) :
    ¬ GoPrimitives.DeadlockedWithQueues g.locks priority ts :=
  GoPrimitives.deadlock_free_with_queues (locks_reachable reachable) priority ts

end SnapshotGate

/-!
## Starvation is not excluded by the lock safety specification

The infinite execution below uses only exclusive mutex requests. Thread 0
waits forever while thread 1 acquires/releases repeatedly. Every transition
satisfies our standard-primitive SAFETY abstraction and no state is deadlocked.
This establishes that safety plus lock ordering is insufficient for liveness.
It is NOT a claim that Go's actual starvation-mode implementation admits this
infinite run: that implementation adds scheduling/handoff behavior not modeled
by the safety specification. Go's Mutex documentation does not promise strict
FIFO, and the runtime implementation is not verified by this Lean file.

RWMutex blocks new readers once a writer is waiting in its reader-drain phase;
that is useful protection, not a proof that every whole tree operation finishes.
Avoid recursive RLock, and prefer a single pessimistic insertion attempt over
an unbounded sequence of optimistic retries when seeking an operation-level
starvation bound.
-/
namespace Starvation

def waiting : State := setWait empty 0 (some ⟨0, .write⟩)
def competing : State := setWait waiting 1 (some ⟨0, .write⟩)
def owned : State := setWait (setHeld competing 1 0 (some .write)) 1 none

private theorem state_ext {a b : State} (held : a.held = b.held)
    (pending : a.waiting = b.waiting) : a = b := by
  cases a
  cases b
  simp_all

theorem unlock_returns_to_waiting : setHeld owned 1 0 none = waiting := by
  apply state_ext
  · funext t n
    simp only [setHeld, owned, competing, waiting, setWait, empty]
    split <;> simp_all
  · funext t
    by_cases ht : t = 1
    · subst t
      rfl
    · simp [setHeld, owned, competing, waiting, setWait, ht]

theorem request_competitor : GoPrimitives.Step waiting competing := by
  apply GoPrimitives.Step.request
  · rfl
  · simp [waiting, setWait, empty]

theorem grant_competitor : GoPrimitives.Step competing owned := by
  apply GoPrimitives.Step.grant competing 1 ⟨0, .write⟩
  · rfl
  · simp [Compatible, competing, waiting, setWait, empty]

theorem release_competitor : GoPrimitives.Step owned waiting := by
  rw [← unlock_returns_to_waiting]
  exact GoPrimitives.Step.unlock _ 1 0 .write rfl rfl

def execution (n : Nat) : State :=
  match n % 3 with
  | 0 => waiting
  | 1 => competing
  | _ => owned

theorem execution_legal (n : Nat) : GoPrimitives.Step (execution n) (execution (n + 1)) := by
  have bound := Nat.mod_lt n (by decide : 0 < 3)
  have cases : n % 3 = 0 ∨ n % 3 = 1 ∨ n % 3 = 2 := by omega
  rcases cases with h | h | h
  · have hn : (n + 1) % 3 = 1 := by omega
    simpa [execution, h, hn] using request_competitor
  · have hn : (n + 1) % 3 = 2 := by omega
    simpa [execution, h, hn] using grant_competitor
  · have hn : (n + 1) % 3 = 0 := by omega
    simpa [execution, h, hn] using release_competitor

theorem execution_reachable (n : Nat) : GoPrimitives.Reachable (execution n) := by
  induction n with
  | zero =>
    exact GoPrimitives.Reachable.next GoPrimitives.Reachable.initial
      (GoPrimitives.Step.request empty 0 ⟨0, .write⟩ rfl (by simp [empty]))
  | succ n ih => exact GoPrimitives.Reachable.next ih (execution_legal n)

theorem always_waiting_never_owner (n : Nat) :
    (execution n).waiting 0 = some ⟨0, .write⟩ ∧ (execution n).held 0 0 = none := by
  unfold execution
  split <;> exact ⟨rfl, rfl⟩

theorem starvation_without_deadlock :
    (∀ n, GoPrimitives.Step (execution n) (execution (n + 1))) ∧
    (∀ n, (execution n).waiting 0 = some ⟨0, .write⟩ ∧ (execution n).held 0 0 = none) ∧
    (∀ n ts, ¬ Deadlocked (execution n) ts) := by
  exact ⟨execution_legal, always_waiting_never_owner,
    fun n ts => GoPrimitives.deadlock_free (execution_reachable n) ts⟩

end Starvation

/-!
## Optional explicit FIFO admission: conditional starvation freedom

An implementable alternative is an explicit queue protected by sync.Mutex,
with one private ready channel per request. Enqueue under the queue mutex;
reserve ownership for the queue head and signal ONLY that request (close its
private channel, or send a buffered token). Unlock the queue mutex before
waiting on a channel. Release hands ownership to the next queued request.
Do not implement this as many waiters racing to receive from a shared token
channel: the channel API alone is not a FIFO lock admission specification.

The model below is exclusive FIFO admission, suitable as a conservative
whole-operation gate (serializing all operations, including snapshot scans).
A batching reader/writer variant would need additional proofs. This queue
model is separate from the standard RWMutex gate above; it does not magically
give that gate a FIFO contract.

Tickets count successful ENQUEUE events, not call start times. The bound is
post-enqueue: new arrivals cannot increase the number of predecessors. Getting
the queue mutex to enqueue still requires progress from that mutex/scheduler.
Finite critical sections and fair dispatch are explicit hypotheses. There is
no unconditional fairness proof for Go's scheduler or channel implementation.
Counters are mathematical naturals; an implementation needs a queue or an
overflow-safe ticket representation. Cancellation/crashes are not modeled.
-/
namespace FIFO

structure Queue where
  issued : Nat
  front : Nat
  busy : Bool
  deriving DecidableEq, Repr

def initial : Queue := ⟨0, 0, false⟩

inductive Step : Queue → Queue → Prop where
  | enqueue (q : Queue) : Step q { q with issued := q.issued + 1 }
  | admit (q : Queue) (free : q.busy = false) (pending : q.front < q.issued) :
      Step q { q with busy := true }
  | release (q : Queue) (owner : q.busy = true) :
      Step q { q with front := q.front + 1, busy := false }
  | idle (q : Queue) : Step q q

def WellFormed (q : Queue) : Prop :=
  q.front ≤ q.issued ∧ (q.busy = true → q.front < q.issued)

theorem step_wellFormed (wf : WellFormed q) (step : Step q q') : WellFormed q' := by
  rcases wf with ⟨bound, busyBound⟩
  cases step with
  | enqueue => simp only [WellFormed]; omega
  | admit free pending => exact ⟨bound, fun _ => pending⟩
  | release owner =>
    have h := busyBound owner
    exact ⟨Nat.succ_le_of_lt h, by simp⟩
  | idle => exact ⟨bound, busyBound⟩

theorem issued_nondecreasing (step : Step q q') : q.issued ≤ q'.issued := by
  cases step <;> simp

theorem front_nondecreasing (step : Step q q') : q.front ≤ q'.front := by
  cases step <;> simp

-- No release skips an earlier ticket; admissions always reserve the front.
theorem no_ticket_skipped (step : Step q q') : q'.front ≤ q.front + 1 := by
  cases step <;> simp

def ahead (q : Queue) (ticket : Nat) : Nat := ticket - q.front

theorem arrivals_cannot_increase_wait (q : Queue) (ticket : Nat) :
    ahead { q with issued := q.issued + 1 } ticket = ahead q ticket := rfl

theorem predecessor_completion_reduces_wait (before : q.front < ticket) :
    ahead { q with front := q.front + 1, busy := false } ticket + 1 = ahead q ticket := by
  simp only [ahead]
  omega

def Valid (run : Nat → Queue) : Prop := ∀ n, Step (run n) (run (n + 1))

-- When the queue has work and no owner, the head is eventually admitted.
def FairDispatch (run : Nat → Queue) : Prop :=
  ∀ n, (run n).busy = false → (run n).front < (run n).issued →
    ∃ m, n ≤ m ∧ (run m).busy = true ∧ (run m).front = (run n).front

-- An admitted owner eventually finishes and releases its reservation.
def OwnersFinish (run : Nat → Queue) : Prop :=
  ∀ n, (run n).busy = true → ∃ m, n < m ∧ (run n).front < (run m).front

theorem issued_monotone (valid : Valid run) (order : n ≤ m) :
    (run n).issued ≤ (run m).issued := by
  induction order with
  | refl => exact Nat.le_refl _
  | @step m order ih => exact Nat.le_trans ih (issued_nondecreasing (valid m))

theorem pending_head_progress (dispatch : FairDispatch run) (finish : OwnersFinish run)
    (pending : (run n).front < (run n).issued) :
    ∃ m, n < m ∧ (run n).front < (run m).front := by
  cases busy : (run n).busy with
  | true => exact finish n busy
  | false =>
    obtain ⟨m, hm, owner, same⟩ := dispatch n busy pending
    obtain ⟨k, hk, advanced⟩ := finish m owner
    refine ⟨k, by omega, ?_⟩
    rw [same] at advanced
    exact advanced

/- Each queued ticket completes eventually, even with indefinitely many new
arrivals. The induction is on the finite number of earlier tickets plus self,
not on a bound on future arrivals. This is a temporal liveness theorem, with
the scheduler/owner assumptions visible in its type. -/
theorem starvation_free_after_enqueue (valid : Valid run)
    (dispatch : FairDispatch run) (finish : OwnersFinish run)
    (queued : ticket < (run n).issued) :
    ∃ m, n ≤ m ∧ ticket < (run m).front := by
  have aux : ∀ distance n, ticket + 1 - (run n).front = distance →
      ticket < (run n).issued → ∃ m, n ≤ m ∧ ticket < (run m).front := by
    intro distance
    induction distance using Nat.strongRecOn with
    | ind distance ih =>
      intro n eq issued
      by_cases done : ticket < (run n).front
      · exact ⟨n, Nat.le_refl _, done⟩
      · have pending : (run n).front < (run n).issued := by omega
        obtain ⟨m, later, advance⟩ := pending_head_progress dispatch finish pending
        have smaller : ticket + 1 - (run m).front < distance := by omega
        have issuedLater := issued_monotone valid (Nat.le_of_lt later)
        obtain ⟨k, hk, served⟩ := ih _ smaller m rfl (Nat.lt_of_lt_of_le issued issuedLater)
        exact ⟨k, Nat.le_trans (Nat.le_of_lt later) hk, served⟩
  exact aux _ n rfl queued

private theorem crossing_requires_owner (step : Step q q')
    (before : q.front ≤ ticket) (after : ticket < q'.front) :
    q.front = ticket ∧ q.busy = true := by
  cases step with
  | enqueue => exact False.elim (Nat.not_lt_of_ge before after)
  | admit => exact False.elim (Nat.not_lt_of_ge before after)
  | idle => exact False.elim (Nat.not_lt_of_ge before after)
  | release owner =>
    change ticket < q.front + 1 at after
    exact ⟨by omega, owner⟩

-- Strengthen eventual completion to an explicit state owning this very ticket.
theorem eventually_owns_its_ticket (valid : Valid run)
    (dispatch : FairDispatch run) (finish : OwnersFinish run)
    (notYetServed : (run n).front ≤ ticket) (queued : ticket < (run n).issued) :
    ∃ m, n ≤ m ∧ (run m).front = ticket ∧ (run m).busy = true := by
  have crossing : ∀ m, n ≤ m → ticket < (run m).front →
      ∃ k, n ≤ k ∧ (run k).front = ticket ∧ (run k).busy = true := by
    intro m later
    induction later with
    | refl => intro past; exact False.elim (Nat.not_lt_of_ge notYetServed past)
    | @step m later ih =>
      intro past
      by_cases already : ticket < (run m).front
      · exact ih already
      · have owner := crossing_requires_owner (valid m) (Nat.le_of_not_gt already) past
        exact ⟨m, later, owner⟩
  obtain ⟨m, later, past⟩ := starvation_free_after_enqueue valid dispatch finish queued
  exact crossing m later past

end FIFO

-- New proof dependency audit, including temporal liveness and snapshot failure.
#print axioms GoPrimitives.race_free
#print axioms GoPrimitives.deadlock_free
#print axioms GoPrimitives.deadlock_free_with_queues
#print axioms SnapshotCounterexample.hand_over_hand_not_snapshot
#print axioms SnapshotGate.snapshot_consistent
#print axioms SnapshotGate.gate_no_lock_deadlock
#print axioms SnapshotGate.gate_no_queue_deadlock
#print axioms Starvation.starvation_without_deadlock
#print axioms FIFO.starvation_free_after_enqueue
#print axioms FIFO.eventually_owns_its_ticket

end BPTreeLocks

