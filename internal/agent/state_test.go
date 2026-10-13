package agent

import "testing"

func TestStateMachineValidTransitions(t *testing.T) {
	sm := NewStateMachine()

	transitions := []State{
		StatePlanning,
		StateExecuting,
		StateObserving,
		StateCompleted,
	}

	for _, next := range transitions {
		if err := sm.Transition(next); err != nil {
			t.Fatalf("transition to %s failed: %v", next, err)
		}
	}

	if got := sm.Current(); got != StateCompleted {
		t.Fatalf("expected completed, got %s", got)
	}

	if got := len(sm.History()); got != 5 {
		t.Fatalf("expected 5 states in history, got %d", got)
	}
}

func TestStateMachineRejectsInvalidTransition(t *testing.T) {
	sm := NewStateMachine()

	if err := sm.Transition(StateCompleted); err == nil {
		t.Fatal("expected pending -> completed to be rejected")
	}

	if got := sm.Current(); got != StatePending {
		t.Fatalf("state should remain pending, got %s", got)
	}
}

func TestTerminalStateCannotTransition(t *testing.T) {
	sm := NewStateMachine()

	for _, next := range []State{
		StatePlanning,
		StateExecuting,
		StateObserving,
		StateCompleted,
	} {
		if err := sm.Transition(next); err != nil {
			t.Fatal(err)
		}
	}

	if err := sm.Transition(StatePlanning); err == nil {
		t.Fatal("completed state must be terminal")
	}
}
