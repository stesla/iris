package telnet

import (
	"math"

	"github.com/stesla/iris/internal/event"
)

type OptionState interface {
	Allow(them, us bool)
	AllowThem(bool)
	AllowUs(bool)
	DisableForThem(d event.Dispatcher)
	DisableForUs(d event.Dispatcher)
	EnableForThem(d event.Dispatcher)
	EnableForUs(d event.Dispatcher)
	EnabledForThem() bool
	EnabledForUs() bool
	Option() byte
}

const EventOption event.Name = "telnet.event.option"

type OptionData struct {
	OptionState
	ChangedThem bool
	ChangedUs   bool
}

type OptionMap interface {
	Get(opt byte) OptionState

	handleNegotiation(data any) error
	set(OptionState)
}

func NewOptionMap(d event.Dispatcher) OptionMap {
	result := &optionMap{
		d: d,
		m: make(map[byte]*optionState, math.MaxUint8),
	}
	for opt := range byte(math.MaxUint8) {
		result.m[opt] = &optionState{opt: opt}
	}
	return result
}

type optionMap struct {
	d event.Dispatcher
	m map[byte]*optionState
}

func (m *optionMap) Get(opt byte) OptionState {
	return m.m[opt]
}

func (m *optionMap) handleNegotiation(data any) error {
	negotiation := data.(*negotiation)
	opt := m.m[negotiation.opt]
	opt.receive(m.d, negotiation.cmd)
	return nil
}

func (m *optionMap) set(opt OptionState) {
	o := opt.(*optionState)
	*m.m[o.opt] = *o
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

func (o *optionState) DisableForThem(d event.Dispatcher) {
	o.disable(d, &o.them, DONT)
}

func (o *optionState) DisableForUs(d event.Dispatcher) {
	o.disable(d, &o.us, WONT)
}

func (o *optionState) EnableForThem(d event.Dispatcher) {
	o.enable(d, &o.them, DO)
}

func (o *optionState) EnableForUs(d event.Dispatcher) {
	o.enable(d, &o.us, WILL)
}

func (o *optionState) EnabledForThem() bool { return o.them == qYes }
func (o *optionState) EnabledForUs() bool   { return o.us == qYes }

func (o *optionState) Option() byte { return o.opt }

func (o *optionState) disable(d event.Dispatcher, state *qState, b byte) {
	switch *state {
	case qNo:
		// ignore
	case qYes:
		*state = qWantNoEmpty
		d.Dispatch(eventSend, o.sendCmd(b))
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

func (o *optionState) enable(d event.Dispatcher, state *qState, b byte) {
	switch *state {
	case qNo:
		*state = qWantYesEmpty
		d.Dispatch(eventSend, o.sendCmd(b))
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

func (o *optionState) receive(d event.Dispatcher, b byte) {
	var themBefore, usBefore = o.them, o.us
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
				d.Dispatch(eventSend, o.sendCmd(accept))
			} else {
				d.Dispatch(eventSend, o.sendCmd(reject))
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
			d.Dispatch(eventSend, o.sendCmd(reject))
		}
	case DONT, WONT:
		switch *state {
		case qNo:
			// ignore
		case qYes:
			*state = qNo
			d.Dispatch(eventSend, o.sendCmd(reject))
		case qWantNoEmpty:
			*state = qNo
		case qWantNoOpposite:
			*state = qWantYesEmpty
			d.Dispatch(eventSend, o.sendCmd(accept))
		case qWantYesEmpty:
			*state = qNo
		case qWantYesOpposite:
			*state = qNo
		}
	}
	if changedThem, changedUs := themBefore != o.them, usBefore != o.us; changedThem || changedUs {
		d.Dispatch(EventOption, OptionData{
			OptionState: o,
			ChangedThem: changedThem,
			ChangedUs:   changedUs,
		})
	}

}

func (o *optionState) sendCmd(b byte) any {
	return []byte{IAC, b, o.opt}
}

const eventNegotation event.Name = "internal.option.negotiation"

type negotiation struct {
	cmd byte
	opt byte
}

const eventSubnegotiation event.Name = "internal.option.subnegotiation"

type subnegotiation struct {
	opt  byte
	data []byte
}
