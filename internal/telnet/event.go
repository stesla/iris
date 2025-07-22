package telnet

import (
	"reflect"
	"sync"
)

type EventListener func(any) error

type EventHandler interface {
	AddEventListener(ev any, eh EventListener)
	HandleEvent(ev any) error
}

func NewEventHandler() EventHandler {
	return &eventHandler{
		handlers: map[reflect.Type][]EventListener{},
	}
}

type eventHandler struct {
	handlers map[reflect.Type][]EventListener
	sync.RWMutex
}

func (eh *eventHandler) AddEventListener(ev any, el EventListener) {
	t := reflect.TypeOf(ev)
	eh.Lock()
	defer eh.Unlock()
	eh.handlers[t] = append(eh.handlers[t], el)
}

func (eh *eventHandler) HandleEvent(ev any) (err error) {
	t := reflect.TypeOf(ev)
	eh.Lock()
	defer eh.Unlock()
	for _, h := range eh.handlers[t] {
		if err = h(ev); err != nil {
			return
		}
	}
	return
}

type eventEOF struct{}

var EventEOF eventEOF

type eventGA struct{}

var EventGA eventGA

type eventNegotiation struct {
	cmd byte
	opt byte
}

type eventSubnegotiation struct {
	opt  byte
	data []byte
}

type eventSend struct {
	data []byte
}
