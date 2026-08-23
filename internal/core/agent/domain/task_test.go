package domain

import "testing"

func TestOnlyFinalStatesAreTerminal(t *testing.T) {
	t.Parallel()
	for _, state := range []State{StateQueued, StateRunning, StateWaiting} {
		if state.Terminal() {
			t.Errorf("%s must remain resumable", state)
		}
	}
	for _, state := range []State{StateCompleted, StateFailed, StateCancelled} {
		if !state.Terminal() {
			t.Errorf("%s must be terminal", state)
		}
	}
}
