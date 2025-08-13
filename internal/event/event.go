package event

import (
	"context"
	"slices"
	"sync"
)

type Name string

type Listener interface {
	Listen(ctx context.Context, data any) error
}

type ListenerFunc func(ctx context.Context, data any) error

func (f ListenerFunc) Listen(ctx context.Context, data any) error {
	return f(ctx, data)
}

type Dispatcher interface {
	Listen(event Name, l Listener)
	ListenFunc(event Name, fn ListenerFunc) Listener
	Dispatch(ctx context.Context, event Name, data any) error
	RemoveListener(event Name, l Listener)
}

func NewDispatcher() Dispatcher {
	return &dispatcher{
		handlers: map[Name][]Listener{},
	}
}

type dispatcher struct {
	handlers map[Name][]Listener
	sync.RWMutex
}

func (d *dispatcher) Listen(event Name, l Listener) {
	d.Lock()
	defer d.Unlock()
	d.handlers[event] = append(d.handlers[event], l)
}

func (d *dispatcher) ListenFunc(event Name, fn ListenerFunc) (l Listener) {
	l = &fn
	d.Listen(event, l)
	return
}

func (d *dispatcher) Dispatch(ctx context.Context, event Name, data any) (err error) {
	d.RLock()
	defer d.RUnlock()
	for _, h := range d.handlers[event] {
		if err = h.Listen(ctx, data); err != nil {
			return
		}
	}
	return
}

func (d *dispatcher) RemoveListener(event Name, l Listener) {
	d.Lock()
	defer d.Unlock()
	d.handlers[event] = slices.DeleteFunc(d.handlers[event], func(ll Listener) bool {
		return l == ll
	})
}
