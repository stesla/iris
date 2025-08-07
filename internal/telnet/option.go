package telnet

import (
	"math"

	"github.com/stesla/iris/internal/event"
)

type OptionState interface {
	Allow(them, us bool)
	AllowThem(bool)
	AllowUs(bool)
	DisableForThem(eh event.Handler)
	DisableForUs(eh event.Handler)
	EnableForThem(eh event.Handler)
	EnableForUs(eh event.Handler)
	EnabledForThem() bool
	EnabledForUs() bool
	Option() byte
}

type OptionMap struct {
	eh event.Handler
	m  map[byte]*optionState
}

func NewOptionMap(eh event.Handler) (result *OptionMap) {
	result = &OptionMap{
		eh: eh,
		m:  make(map[byte]*optionState, math.MaxUint8),
	}
	for opt := range byte(math.MaxUint8) {
		result.m[opt] = &optionState{opt: opt}
	}
	return
}

func (m *OptionMap) Get(opt byte) OptionState {
	return m.m[opt]
}

func (m *OptionMap) handleNegotiation(data any) error {
	negotiation := data.(*negotiation)
	opt := m.m[negotiation.opt]
	opt.receive(m.eh, negotiation.cmd)
	return nil
}

type qState int

const (
	qNo qState = 0 + iota
	qYes
	qWantNoEmpty
	qWantNoOpposite
	qWantYesEmpty
	qWantYesOpposite
)

type optionState struct {
	opt       byte
	allowThem bool
	them      qState
	allowUs   bool
	us        qState
}

func (o *optionState) Allow(them, us bool) {
	o.AllowThem(them)
	o.AllowUs(us)
}

func (o *optionState) AllowThem(allow bool) {
	o.allowThem = allow
}

func (o *optionState) AllowUs(allow bool) {
	o.allowUs = allow
}

func (o *optionState) DisableForThem(eh event.Handler) {
	o.disable(eh, &o.them, DONT)
}

func (o *optionState) DisableForUs(eh event.Handler) {
	o.disable(eh, &o.us, WONT)
}

func (o *optionState) EnableForThem(eh event.Handler) {
	o.enable(eh, &o.them, DO)
}

func (o *optionState) EnableForUs(eh event.Handler) {
	o.enable(eh, &o.us, WILL)
}

func (o *optionState) EnabledForThem() bool { return o.them == qYes }
func (o *optionState) EnabledForUs() bool   { return o.us == qYes }

func (o *optionState) Option() byte { return o.opt }

func (o *optionState) disable(eh event.Handler, state *qState, b byte) {
	switch *state {
	case qNo:
		// ignore
	case qYes:
		*state = qWantNoEmpty
		eh.HandleEvent(eventSend, o.sendCmd(b))
	case qWantNoEmpty:
		// ignore
	case qWantNoOpposite:
		*state = qWantNoEmpty
	case qWantYesEmpty:
		*state = qWantYesOpposite
	case qWantYesOpposite:
		// ignore
	}
}

func (o *optionState) enable(eh event.Handler, state *qState, b byte) {
	switch *state {
	case qNo:
		*state = qWantYesEmpty
		eh.HandleEvent(eventSend, o.sendCmd(b))
	case qYes:
		// ignore
	case qWantNoEmpty:
		*state = qWantNoOpposite
	case qWantNoOpposite:
		// ignore
	case qWantYesEmpty:
		// ignore
	case qWantYesOpposite:
		*state = qWantYesEmpty
	}
}

func (o *optionState) receive(eh event.Handler, b byte) {
	var allow *bool
	var state *qState
	var accept byte
	var reject byte
	switch b {
	case DO, DONT:
		allow = &o.allowUs
		state = &o.us
		accept = WILL
		reject = WONT
	case WILL, WONT:
		allow = &o.allowThem
		state = &o.them
		accept = DO
		reject = DONT
	}
	switch b {
	case DO, WILL:
		switch *state {
		case qNo:
			if *allow {
				*state = qYes
				eh.HandleEvent(eventSend, o.sendCmd(accept))
			} else {
				eh.HandleEvent(eventSend, o.sendCmd(reject))
			}
		case qYes:
			// ignore
		case qWantNoEmpty:
			*state = qNo
		case qWantNoOpposite:
			*state = qYes
		case qWantYesEmpty:
			*state = qYes
		case qWantYesOpposite:
			*state = qWantNoEmpty
			eh.HandleEvent(eventSend, o.sendCmd(reject))
		}
	case DONT, WONT:
		switch *state {
		case qNo:
			// ignore
		case qYes:
			*state = qNo
			eh.HandleEvent(eventSend, o.sendCmd(reject))
		case qWantNoEmpty:
			*state = qNo
		case qWantNoOpposite:
			*state = qWantYesEmpty
			eh.HandleEvent(eventSend, o.sendCmd(accept))
		case qWantYesEmpty:
			*state = qNo
		case qWantYesOpposite:
			*state = qNo
		}
	}
}

func (o *optionState) sendCmd(b byte) any {
	return []byte{IAC, b, o.opt}
}
