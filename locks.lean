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

/- Assessment and remaining proof obligations

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
   pointer handles this example. No snapshot/linearizability claim is proved.
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

end BPTreeLocks

