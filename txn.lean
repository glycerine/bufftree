import Std

/-!
Transaction-level obligations for txn_design.md / tx.go.

This model proves lock capability exclusion, read-ready publication, span
stability for an admitted reader, and reverse journal restoration. The lock's
admission is an abstract service; scheduler/library progress is a premise, not
an axiom. This does NOT verify Go compilation/runtime, concrete BPA ordering,
key equivalence, cursor enumeration, or split/merge refinement. Those are
covered by implementation tests, not claimed as machine-checked refinements.

The journal model uses arbitrary logical state and reversible operations. A
single-key binding inverse is instantiated below. Clear's inverse saves the
removed map. No whole-map copy is required for ordinary updates.
-/
namespace Transactions

abbrev Map := Nat → Option Nat
structure State where
  readers : Nat
  writer : Bool
  ready : Bool
  data : Map
  layout : Nat

def Safe (s : State) : Prop :=
  (s.writer = true → s.readers = 0) ∧
  (s.writer = false → s.ready = true)

inductive Step : State → State → Prop where
  | readEnter (s) (h : s.writer = false) :
      Step s { s with readers := s.readers + 1 }
  | readExit (s) (n) (h : s.readers = n + 1) :
      Step s { s with readers := n }
  | writeEnter (s) (h : s.writer = false) (empty : s.readers = 0) :
      Step s { s with writer := true }
  | mutate (s) (h : s.writer = true) (m : Map) (l : Nat) :
      Step s { s with data := m, layout := l, ready := false }
  | prepare (s) (h : s.writer = true) (l : Nat) :
      Step s { s with layout := l, ready := true }
  | writeExit (s) (h : s.writer = true) (prepared : s.ready = true) :
      Step s { s with writer := false }
  | local (s) : Step s s

theorem preserves_safe {a b : State} (safe : Safe a) (step : Step a b) : Safe b := by
  rcases safe with ⟨exclusive, ready⟩
  cases step <;> simp_all [Safe]

def initial : State := ⟨0, false, true, fun _ => none, 0⟩

inductive Reachable : State → Prop where
  | init : Reachable initial
  | next {a b} : Reachable a → Step a b → Reachable b

theorem reachable_safe {s : State} (h : Reachable s) : Safe s := by
  induction h with
  | init => simp [Safe, initial]
  | next _ step ih => exact preserves_safe ih step

theorem reader_writer_exclusion {s : State} (safe : Safe s)
    (reader : 0 < s.readers) : s.writer = false := by
  cases h : s.writer
  · rfl
  · have := safe.1 h; omega

theorem admitted_read_ready {s : State} (safe : Safe s)
    (reader : 0 < s.readers) : s.ready = true :=
  safe.2 (reader_writer_exclusion safe reader)

-- A reader already holding a capability excludes every storage-changing step.
theorem reader_step_frozen {a b : State} (safe : Safe a)
    (reader : 0 < a.readers) (step : Step a b) :
    b.data = a.data ∧ b.layout = a.layout := by
  have noWriter := reader_writer_exclusion safe reader
  cases step <;> simp_all

-- Each edge starts with the particular reader still admitted. New readers may
-- enter, other readers may leave; the protected reader exits only after span end.
inductive ReadSpan : State → State → Prop where
  | refl (s) : ReadSpan s s
  | cons {a b c} (reader : 0 < a.readers) (step : Step a b)
      (rest : ReadSpan b c) : ReadSpan a c

theorem whole_span_stable {a b : State} (safe : Safe a) (span : ReadSpan a b) :
    b.data = a.data ∧ b.layout = a.layout := by
  induction span with
  | refl => exact ⟨rfl, rfl⟩
  | cons reader step rest ih =>
    have one := reader_step_frozen safe reader step
    have tail := ih (preserves_safe safe step)
    exact ⟨tail.1.trans one.1, tail.2.trans one.2⟩

-- Begin/end are the ONLY lock requests. Transaction methods are storage or
-- local steps; they never request a second capability. Application-level
-- nested transactions and unbounded callbacks are outside this protocol.
inductive Method : State → State → Prop where
  | mutate (s) (h : s.writer = true) (m : Map) (l : Nat) :
      Method s { s with data := m, layout := l, ready := false }
  | prepare (s) (h : s.writer = true) (l : Nat) :
      Method s { s with layout := l, ready := true }
  | local (s) : Method s s

theorem methods_preserve_ownership {a b : State} (h : Method a b) :
    b.readers = a.readers ∧ b.writer = a.writer := by
  cases h <;> exact ⟨rfl, rfl⟩

-- In the single-database protocol, an application thread requests admission
-- only when it has no capability. A holder executes methods or releases; it
-- never requests admission again. Callback-created external waits are excluded.
inductive Phase where | idle | waitingRead | waitingWrite | reading | writing
  deriving DecidableEq

def Waiting (p : Phase) : Prop := p = .waitingRead ∨ p = .waitingWrite

def Holding (p : Phase) : Prop := p = .reading ∨ p = .writing

def WaitsFor (threads : Nat → Phase) (a b : Nat) : Prop :=
  Waiting (threads a) ∧ Holding (threads b) ∧
  (threads a = .waitingWrite ∨ threads b = .writing)

theorem holder_not_waiting {p : Phase} (h : Holding p) : ¬ Waiting p := by
  cases p <;> simp_all [Holding, Waiting]

inductive WaitPath (threads : Nat → Phase) : Nat → Nat → Prop where
  | edge {a b} : WaitsFor threads a b → WaitPath threads a b
  | cons {a b c} : WaitsFor threads a b → WaitPath threads b c → WaitPath threads a c

theorem path_starts_waiting {threads : Nat → Phase} {a b : Nat}
    (p : WaitPath threads a b) : Waiting (threads a) := by
  cases p with
  | edge h => exact h.1
  | cons h _ => exact h.1

theorem no_internal_wait_cycle (threads : Nat → Phase) (a : Nat) :
    ¬ WaitPath threads a a := by
  intro path
  cases path with
  | edge h => exact holder_not_waiting h.2.1 h.1
  | cons h rest => exact holder_not_waiting h.2.1 (path_starts_waiting rest)

-- First terminal action wins. Managed handles end independently of outer lock
-- ownership; only callback unwind releases managed ownership.
inductive Status where | open | committed | rolledBack
  deriving DecidableEq
structure Lifetime where
  status : Status
  managed : Bool
  held : Bool
  resources : Nat
  deriving DecidableEq

def finish (s : Lifetime) (result : Status) : Lifetime :=
  if s.status = .open then
    { s with status := result, held := s.managed && s.held, resources := 0 }
  else s

theorem terminal_idempotent (s : Lifetime) (a b : Status) (ha : a ≠ .open) :
    finish (finish s a) b = finish s a := by
  by_cases h : s.status = .open <;> simp [finish, h, ha]

theorem managed_keeps_lock (s : Lifetime) (a : Status)
    (managed : s.managed = true) : (finish s a).held = s.held := by
  simp [finish, managed]; split <;> rfl

theorem finish_closes_resources (s : Lifetime) (a : Status)
    (h : s.status = .open) : (finish s a).resources = 0 := by
  simp [finish, h]

def set (m : Map) (k : Nat) (v : Option Nat) : Map :=
  fun j => if j = k then v else m j

theorem undo_binding (m : Map) (k : Nat) (v : Option Nat) :
    set (set m k v) k (m k) = m := by
  funext j
  by_cases h : j = k <;> simp [set, h]

structure Edit (α : Type) where
  apply : α → α
  inverse : α → α → α -- prior logical state determines the journal record
  restores : ∀ s, inverse s (apply s) = s

def bindingEdit (k : Nat) (v : Option Nat) : Edit Map where
  apply m := set m k v
  inverse before after := set after k (before k)
  restores m := undo_binding m k v

def clearEdit : Edit Map where
  apply _ := fun _ => none
  inverse before _ := before
  restores _ := rfl

-- Execute chronological edits, accumulating inverse functions at the head.
def run {α : Type} : List (Edit α) → α → α × List (α → α)
  | [], s => (s, [])
  | e :: es, s =>
      let (last, journal) := run es (e.apply s)
      (last, journal ++ [e.inverse s])

def replay {α : Type} (journal : List (α → α)) (s : α) : α :=
  journal.foldl (fun current undo => undo current) s

theorem replay_append {α : Type} (xs ys : List (α → α)) (s : α) :
    replay (xs ++ ys) s = replay ys (replay xs s) := by
  simp [replay, List.foldl_append]

theorem rollback_restores {α : Type} (edits : List (Edit α)) (s : α) :
    replay (run edits s).2 (run edits s).1 = s := by
  induction edits generalizing s with
  | nil => rfl
  | cons e es ih =>
    simp only [run]
    rw [replay_append, ih]
    exact e.restores s

-- The membership filter is only a negative lookup shortcut. Collisions may
-- cause extra exact searches; they may never suppress a present key. This is
-- an abstract bit-set invariant, not a verification of maphash or its layout.
namespace Membership
abbrev Bits := Nat → Bool
variable {K : Type}

def Sound (positions : K → Nat × Nat) (live : K → Prop) (bits : Bits) : Prop :=
  ∀ k, live k → bits (positions k).1 = true ∧ bits (positions k).2 = true

def maybePresent (positions : K → Nat × Nat) (bits : Bits) (k : K) : Bool :=
  bits (positions k).1 && bits (positions k).2

def remember (positions : K → Nat × Nat) (bits : Bits) (k : K) : Bits :=
  fun i => bits i || i == (positions k).1 || i == (positions k).2

theorem negative_is_absent (positions : K → Nat × Nat) (live : K → Prop)
    (bits : Bits) (sound : Sound positions live bits) (k : K)
    (negative : maybePresent positions bits k = false) : ¬ live k := by
  intro present
  have h := sound k present
  simp [maybePresent, h.1, h.2] at negative

theorem insert_preserves_sound (positions : K → Nat × Nat) (live : K → Prop)
    (bits : Bits) (sound : Sound positions live bits) (k : K) :
    Sound positions (fun q => q = k ∨ live q) (remember positions bits k) := by
  intro q h
  rcases h with rfl | present
  · simp [remember]
  · have h := sound q present
    simp [remember, h.1, h.2]

theorem deletion_preserves_sound (positions : K → Nat × Nat)
    (before after : K → Prop) (bits : Bits) (sound : Sound positions before bits)
    (subset : ∀ k, after k → before k) : Sound positions after bits := by
  intro k h
  exact sound k (subset k h)
end Membership

-- Conditional lock admission is explicit, without asserting strict FIFO.
def AdmissionProgress (pending admitted : Nat → Prop) : Prop :=
  ∀ n, pending n → ∃ m, n ≤ m ∧ admitted m

theorem pending_eventually_admitted (pending admitted : Nat → Prop)
    (progress : AdmissionProgress pending admitted) (n : Nat) (h : pending n) :
    ∃ m, n ≤ m ∧ admitted m := progress n h

#print axioms Membership.negative_is_absent
#print axioms Membership.insert_preserves_sound
#print axioms reachable_safe
#print axioms preserves_safe
#print axioms whole_span_stable
#print axioms methods_preserve_ownership
#print axioms no_internal_wait_cycle
#print axioms terminal_idempotent
#print axioms rollback_restores
end Transactions
