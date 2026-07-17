package gameserver

import "testing"

func TestGameServerStateTransitions(t *testing.T) {
	tests := []struct {
		current State
		next    State
		want    bool
	}{
		{StateStarting, StateReady, true},
		{StateStarting, StateRunning, false},
		{StateReady, StateRunning, true},
		{StateUnhealthy, StateReady, true},
		{StateDraining, StateReady, false},
		{StateOffline, StateReady, false},
	}
	for _, test := range tests {
		if got := validTransition(test.current, test.next); got != test.want {
			t.Errorf("validTransition(%s, %s) = %v", test.current, test.next, got)
		}
	}
}
