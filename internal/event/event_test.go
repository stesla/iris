package event

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

const testEvent Name = "test.event"

func TestEventDispatch(t *testing.T) {
	var event Event
	bus := NewDispatcher()
	bus.ListenFunc(testEvent, func(_ context.Context, ev Event) (err error) {
		event = ev
		return nil
	})
	err := bus.Dispatch(context.Background(), Event{testEvent, 42})
	require.NoError(t, err)
	require.Equal(t, testEvent, event.Name)
	require.Equal(t, 42, event.Data)
}

func TestRemoveListener(t *testing.T) {
	var called bool
	fn := func(context.Context, Event) error {
		called = true
		return nil
	}

	bus := NewDispatcher()
	l := bus.ListenFunc(testEvent, fn)
	bus.RemoveListener(testEvent, l)
	err := bus.Dispatch(context.Background(), Event{testEvent, 42})
	require.NoError(t, err)
	require.False(t, called)
}
