package event

import (
	"sync"
)

type Name string

type Listener func(any) error

type Handler interface {
	AddEventListener(event Name, l Listener)
	HandleEvent(event Name, data any) error
}

func NewHandler() Handler {
	return &eventHandler{
		handlers: map[Name][]Listener{},
	}
}

type eventHandler struct {
	handlers map[Name][]Listener
	sync.RWMutex
}

func (eh *eventHandler) AddEventListener(event Name, l Listener) {
	eh.Lock()
	defer eh.Unlock()
	eh.handlers[event] = append(eh.handlers[event], l)
}

func (eh *eventHandler) HandleEvent(event Name, data any) (err error) {
	eh.Lock()
	defer eh.Unlock()
	for _, h := range eh.handlers[event] {
		if err = h(data); err != nil {
			return
		}
	}
	return
}
