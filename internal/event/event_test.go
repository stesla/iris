package event

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const testEvent Name = "test.event"

func TestEventDispatch(t *testing.T) {
	var event any
	bus := NewDispatcher()
	bus.ListenFunc(testEvent, func(ev any) (err error) {
		event = ev
		return nil
	})
	err := bus.Dispatch(testEvent, 42)
	require.NoError(t, err)
	require.Equal(t, 42, event)
}
