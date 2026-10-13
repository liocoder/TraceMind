package agent

import "fmt"

type State string

const (
	StatePending   State = "pending"
	StatePlanning  State = "planning"
	StateExecuting State = "executing"
	StateObserving State = "observing"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
)

type StateMachine struct {
	current State
	history []State
}

func NewStateMachine() *StateMachine {
	return &StateMachine{
		current: StatePending,
		history: []State{StatePending},
	}
}

func (sm *StateMachine) Current() State {
	return sm.current
}

func (sm *StateMachine) History() []State {
	return append([]State(nil), sm.history...)
}

func (sm *StateMachine) Transition(next State) error {
	if !isValidTransition(sm.current, next) {
		return fmt.Errorf(
			"invalid state transition: %s -> %s",
			sm.current,
			next,
		)
	}

	sm.current = next
	sm.history = append(sm.history, next)
	return nil
}

func isValidTransition(from, to State) bool {
	switch from {
	case StatePending:
		return to == StatePlanning || to == StateFailed

	case StatePlanning:
		return to == StateExecuting ||
			to == StateCompleted ||
			to == StateFailed

	case StateExecuting:
		return to == StateObserving || to == StateFailed

	case StateObserving:
		return to == StatePlanning ||
			to == StateCompleted ||
			to == StateFailed

	default:
		// Completed and Failed are terminal states.
		return false
	}
}
