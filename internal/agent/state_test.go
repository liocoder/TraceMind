package agent

import (
	"reflect"
	"strings"
	"testing"
)

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

	want := []State{
		StatePending,
		StatePlanning,
		StateExecuting,
		StateObserving,
		StateCompleted,
	}

	if got := sm.History(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected state history: got %v, want %v", got, want)
	}
}

func TestStateMachineRejectsInvalidTransition(t *testing.T) {
	tests := []struct {
		name string
		from State
		to   State
	}{
		{"pending to executing", StatePending, StateExecuting},
		{"pending to observing", StatePending, StateObserving},
		{"pending to completed", StatePending, StateCompleted},
		{"planning to observing", StatePlanning, StateObserving},
		{"executing to planning", StateExecuting, StatePlanning},
		{"executing to completed", StateExecuting, StateCompleted},
		{"observing to executing", StateObserving, StateExecuting},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewStateMachine()

			// 先将状态机推进到目标起始状态。
			switch tt.from {
			case StatePending:
				// 初始状态，无需转换。
			case StatePlanning:
				if err := sm.Transition(StatePlanning); err != nil {
					t.Fatal(err)
				}
			case StateExecuting:
				if err := sm.Transition(StatePlanning); err != nil {
					t.Fatal(err)
				}
				if err := sm.Transition(StateExecuting); err != nil {
					t.Fatal(err)
				}
			case StateObserving:
				if err := sm.Transition(StatePlanning); err != nil {
					t.Fatal(err)
				}
				if err := sm.Transition(StateExecuting); err != nil {
					t.Fatal(err)
				}
				if err := sm.Transition(StateObserving); err != nil {
					t.Fatal(err)
				}
			}

			beforeHistory := sm.History()
			beforeState := sm.Current()

			err := sm.Transition(tt.to)
			if err == nil {
				t.Fatalf("expected transition %s -> %s to fail", tt.from, tt.to)
			}

			if !strings.Contains(err.Error(), "invalid state transition") {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := sm.Current(); got != beforeState {
				t.Fatalf("state changed after rejected transition: got %s, want %s", got, beforeState)
			}

			if got := sm.History(); !reflect.DeepEqual(got, beforeHistory) {
				t.Fatalf("history changed after rejected transition: got %v, want %v", got, beforeHistory)
			}
		})
	}
}

func TestFailedStateIsTerminal(t *testing.T) {
	sm := NewStateMachine()

	if err := sm.Transition(StateFailed); err != nil {
		t.Fatalf("pending -> failed should be valid: %v", err)
	}

	if got := sm.Current(); got != StateFailed {
		t.Fatalf("expected failed, got %s", got)
	}

	for _, next := range []State{
		StatePending,
		StatePlanning,
		StateExecuting,
		StateObserving,
		StateCompleted,
		StateFailed,
	} {
		if err := sm.Transition(next); err == nil {
			t.Errorf("failed -> %s should be rejected", next)
		}
	}
}

func TestCompletedStateIsTerminal(t *testing.T) {
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

	for _, next := range []State{
		StatePending,
		StatePlanning,
		StateExecuting,
		StateObserving,
		StateCompleted,
		StateFailed,
	} {
		if err := sm.Transition(next); err == nil {
			t.Errorf("completed -> %s should be rejected", next)
		}
	}
}

func TestStateHistoryCannotBeModifiedExternally(t *testing.T) {
	sm := NewStateMachine()

	history := sm.History()
	history[0] = StateFailed

	if got := sm.History()[0]; got != StatePending {
		t.Fatalf("external modification changed internal history: got %s", got)
	}
}

func TestStateMachineInitialState(t *testing.T) {
	sm := NewStateMachine()

	if got := sm.Current(); got != StatePending {
		t.Fatalf("expected initial state pending, got %s", got)
	}

	want := []State{StatePending}
	if got := sm.History(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected initial history: got %v, want %v", got, want)
	}
}
