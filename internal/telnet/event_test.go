package telnet

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type testEvent struct {
	val int
}

func TestEventDispatch(t *testing.T) {
	var event any
	bus := NewEventHandler()
	bus.AddEventListener(&testEvent{}, func(ev any) (err error) {
		event = ev
		return nil
	})
	err := bus.HandleEvent(&testEvent{42})
	require.NoError(t, err)
	require.Equal(t, &testEvent{42}, event)
}
