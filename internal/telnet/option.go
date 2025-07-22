package telnet

type OptionState interface {
	DisableForThem(eh EventHandler)
	DisableForUs(eh EventHandler)
	EnableForThem(eh EventHandler)
	EnableForUs(eh EventHandler)
	EnabledForThem() bool
	EnabledForUs() bool
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

func (o *optionState) DisableForThem(eh EventHandler) {
	o.disable(eh, &o.them, DONT)
}

func (o *optionState) DisableForUs(eh EventHandler) {
	o.disable(eh, &o.us, WONT)
}

func (o *optionState) EnableForThem(eh EventHandler) {
	o.enable(eh, &o.them, DO)
}

func (o *optionState) EnableForUs(eh EventHandler) {
	o.enable(eh, &o.us, WILL)
}

func (o *optionState) EnabledForThem() bool { return o.them == qYes }
func (o *optionState) EnabledForUs() bool   { return o.us == qYes }

func (o *optionState) disable(eh EventHandler, state *qState, b byte) {
	switch *state {
	case qNo:
		// ignore
	case qYes:
		*state = qWantNoEmpty
		eh.HandleEvent(o.sendCmd(b))
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

func (o *optionState) enable(eh EventHandler, state *qState, b byte) {
	switch *state {
	case qNo:
		*state = qWantYesEmpty
		eh.HandleEvent(o.sendCmd(b))
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

func (o *optionState) receive(eh EventHandler, b byte) {
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
				eh.HandleEvent(o.sendCmd(accept))
			} else {
				eh.HandleEvent(o.sendCmd(reject))
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
			eh.HandleEvent(o.sendCmd(reject))
		}
	case DONT, WONT:
		switch *state {
		case qNo:
			// ignore
		case qYes:
			*state = qNo
			eh.HandleEvent(o.sendCmd(reject))
		case qWantNoEmpty:
			*state = qNo
		case qWantNoOpposite:
			*state = qWantYesEmpty
			eh.HandleEvent(o.sendCmd(accept))
		case qWantYesEmpty:
			*state = qNo
		case qWantYesOpposite:
			*state = qNo
		}
	}
}

func (o *optionState) sendCmd(b byte) any {
	return &eventSend{[]byte{IAC, b, o.opt}}
}
