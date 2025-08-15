package telnet

import (
	"context"
	"math"

	"github.com/stesla/iris/internal/event"
)

type OptionState interface {
	Allow(them, us bool)
	AllowThem(bool)
	AllowUs(bool)
	DisableForThem(ctx context.Context)
	DisableForUs(ctx context.Context)
	EnableForThem(ctx context.Context)
	EnableForUs(ctx context.Context)
	EnabledForThem() bool
	EnabledForUs() bool
	Option() byte
}

type OptionMap interface {
	event.Listener
	Get(opt byte) OptionState
	set(OptionState)
}

func NewOptionMap() OptionMap {
	result := &optionMap{
		m: make(map[byte]*optionState, math.MaxUint8),
	}
	for opt := range byte(math.MaxUint8) {
		result.m[opt] = &optionState{opt: opt}
	}
	return result
}

type optionMap struct {
	m map[byte]*optionState
}

func (m *optionMap) Get(opt byte) OptionState {
	return m.m[opt]
}

func (m *optionMap) Listen(ctx context.Context, ev event.Event) error {
	negotiation := ev.Data.(Negotiation)
	opt := m.m[negotiation.Opt]
	opt.receive(ctx, negotiation.Cmd)
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

func (o *optionState) DisableForThem(ctx context.Context) {
	o.disable(ctx, &o.them, DONT)
}

func (o *optionState) DisableForUs(ctx context.Context) {
	o.disable(ctx, &o.us, WONT)
}

func (o *optionState) EnableForThem(ctx context.Context) {
	o.enable(ctx, &o.them, DO)
}

func (o *optionState) EnableForUs(ctx context.Context) {
	o.enable(ctx, &o.us, WILL)
}

func (o *optionState) EnabledForThem() bool { return o.them == qYes }
func (o *optionState) EnabledForUs() bool   { return o.us == qYes }

func (o *optionState) Option() byte { return o.opt }

func (o *optionState) disable(ctx context.Context, state *qState, b byte) {
	switch *state {
	case qNo:
		// ignore
	case qYes:
		*state = qWantNoEmpty
		dispatch(ctx, event.Event{Name: EventSend, Data: o.sendCmd(b)})
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

func (o *optionState) enable(ctx context.Context, state *qState, b byte) {
	switch *state {
	case qNo:
		*state = qWantYesEmpty
		dispatch(ctx, event.Event{Name: EventSend, Data: o.sendCmd(b)})
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

func (o *optionState) receive(ctx context.Context, b byte) {
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
				dispatch(ctx, event.Event{Name: EventSend, Data: o.sendCmd(accept)})
			} else {
				dispatch(ctx, event.Event{Name: EventSend, Data: o.sendCmd(reject)})
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
			dispatch(ctx, event.Event{Name: EventSend, Data: o.sendCmd(reject)})
		}
	case DONT, WONT:
		switch *state {
		case qNo:
			// ignore
		case qYes:
			*state = qNo
			dispatch(ctx, event.Event{Name: EventSend, Data: o.sendCmd(reject)})
		case qWantNoEmpty:
			*state = qNo
		case qWantNoOpposite:
			*state = qWantYesEmpty
			dispatch(ctx, event.Event{Name: EventSend, Data: o.sendCmd(accept)})
		case qWantYesEmpty:
			*state = qNo
		case qWantYesOpposite:
			*state = qNo
		}
	}
	if changedThem, changedUs := themBefore != o.them, usBefore != o.us; changedThem || changedUs {
		dispatch(ctx, event.Event{Name: EventOption, Data: OptionData{
			OptionState: o,
			ChangedThem: changedThem,
			ChangedUs:   changedUs,
		}})
	}

}

func (o *optionState) sendCmd(b byte) any {
	return []byte{IAC, b, o.opt}
}
