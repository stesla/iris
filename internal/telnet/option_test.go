package telnet

import (
	"testing"

	"github.com/stesla/iris/internal/event"
	"github.com/stretchr/testify/require"
)

func TestOptionStateReceive(t *testing.T) {
	var tests = []struct {
		b     byte
		start optionState
		end   optionState
		ev    any
	}{
		{DO, optionState{allowUs: true, us: qNo}, optionState{allowUs: true, us: qYes}, []byte{IAC, WILL}},
		{DO, optionState{us: qNo}, optionState{us: qNo}, []byte{IAC, WONT}},
		{DO, optionState{us: qYes}, optionState{us: qYes}, nil},
		{DO, optionState{us: qWantNoEmpty}, optionState{us: qNo}, nil},
		{DO, optionState{us: qWantNoOpposite}, optionState{us: qYes}, nil},
		{DO, optionState{us: qWantYesEmpty}, optionState{us: qYes}, nil},
		{DO, optionState{us: qWantYesOpposite}, optionState{us: qWantNoEmpty}, []byte{IAC, WONT}},

		{DONT, optionState{us: qNo}, optionState{us: qNo}, nil},
		{DONT, optionState{us: qYes}, optionState{us: qNo}, []byte{IAC, WONT}},
		{DONT, optionState{us: qWantNoEmpty}, optionState{us: qNo}, nil},
		{DONT, optionState{us: qWantNoOpposite}, optionState{us: qWantYesEmpty}, []byte{IAC, WILL}},
		{DONT, optionState{us: qWantYesEmpty}, optionState{us: qNo}, nil},
		{DONT, optionState{us: qWantYesOpposite}, optionState{us: qNo}, nil},

		{WILL, optionState{allowThem: true, them: qNo}, optionState{allowThem: true, them: qYes}, []byte{IAC, DO}},
		{WILL, optionState{them: qNo}, optionState{them: qNo}, []byte{IAC, DONT}},
		{WILL, optionState{them: qYes}, optionState{them: qYes}, nil},
		{WILL, optionState{them: qWantNoEmpty}, optionState{them: qNo}, nil},
		{WILL, optionState{them: qWantNoOpposite}, optionState{them: qYes}, nil},
		{WILL, optionState{them: qWantYesEmpty}, optionState{them: qYes}, nil},
		{WILL, optionState{them: qWantYesOpposite}, optionState{them: qWantNoEmpty}, []byte{IAC, DONT}},

		{WONT, optionState{them: qNo}, optionState{them: qNo}, nil},
		{WONT, optionState{them: qYes}, optionState{them: qNo}, []byte{IAC, DONT}},
		{WONT, optionState{them: qWantNoEmpty}, optionState{them: qNo}, nil},
		{WONT, optionState{them: qWantNoOpposite}, optionState{them: qWantYesEmpty}, []byte{IAC, DO}},
		{WONT, optionState{them: qWantYesEmpty}, optionState{them: qNo}, nil},
		{WONT, optionState{them: qWantYesOpposite}, optionState{them: qNo}, nil},
	}

	for i, test := range tests {
		var eventReceived any
		d := event.NewDispatcher()
		d.ListenFunc(eventSend, func(ev any) error {
			eventReceived = ev
			return nil
		})
		state := test.start
		state.opt = Echo
		expected := test.end
		expected.opt = Echo
		state.receive(d, test.b)
		require.Equal(t, expected, state, i)
		if data, ok := test.ev.([]byte); ok {
			data = append(data, Echo)
			require.Equal(t, data, eventReceived, i)
		} else {
			require.Nil(t, eventReceived, i)
		}
	}
}

func TestOptionEnableOrDisable(t *testing.T) {
	var eventReceived any
	d := event.NewDispatcher()
	d.ListenFunc(eventSend, func(ev any) error {
		eventReceived = ev
		return nil
	})

	disableThem := func(os *optionState) { os.DisableForThem(d) }
	disableUs := func(os *optionState) { os.DisableForUs(d) }
	enableThem := func(os *optionState) { os.EnableForThem(d) }
	enableUs := func(os *optionState) { os.EnableForUs(d) }
	var tests = []struct {
		fn    func(*optionState)
		start optionState
		end   optionState
		ev    any
	}{
		{disableThem, optionState{them: qNo}, optionState{them: qNo}, nil},
		{disableThem, optionState{them: qYes}, optionState{them: qWantNoEmpty}, []byte{IAC, DONT}},
		{disableThem, optionState{them: qWantNoEmpty}, optionState{them: qWantNoEmpty}, nil},
		{disableThem, optionState{them: qWantNoOpposite}, optionState{them: qWantNoEmpty}, nil},
		{disableThem, optionState{them: qWantYesEmpty}, optionState{them: qWantYesOpposite}, nil},
		{disableThem, optionState{them: qWantYesOpposite}, optionState{them: qWantYesOpposite}, nil},

		{disableUs, optionState{us: qNo}, optionState{us: qNo}, nil},
		{disableUs, optionState{us: qYes}, optionState{us: qWantNoEmpty}, []byte{IAC, WONT}},
		{disableUs, optionState{us: qWantNoEmpty}, optionState{us: qWantNoEmpty}, nil},
		{disableUs, optionState{us: qWantNoOpposite}, optionState{us: qWantNoEmpty}, nil},
		{disableUs, optionState{us: qWantYesEmpty}, optionState{us: qWantYesOpposite}, nil},
		{disableUs, optionState{us: qWantYesOpposite}, optionState{us: qWantYesOpposite}, nil},

		{enableThem, optionState{them: qNo}, optionState{them: qWantYesEmpty}, []byte{IAC, DO}},
		{enableThem, optionState{them: qYes}, optionState{them: qYes}, nil},
		{enableThem, optionState{them: qWantNoEmpty}, optionState{them: qWantNoOpposite}, nil},
		{enableThem, optionState{them: qWantNoOpposite}, optionState{them: qWantNoOpposite}, nil},
		{enableThem, optionState{them: qWantYesEmpty}, optionState{them: qWantYesEmpty}, nil},
		{enableThem, optionState{them: qWantYesOpposite}, optionState{them: qWantYesEmpty}, nil},

		{enableUs, optionState{us: qNo}, optionState{us: qWantYesEmpty}, []byte{IAC, WILL}},
		{enableUs, optionState{us: qYes}, optionState{us: qYes}, nil},
		{enableUs, optionState{us: qWantNoEmpty}, optionState{us: qWantNoOpposite}, nil},
		{enableUs, optionState{us: qWantNoOpposite}, optionState{us: qWantNoOpposite}, nil},
		{enableUs, optionState{us: qWantYesEmpty}, optionState{us: qWantYesEmpty}, nil},
		{enableUs, optionState{us: qWantYesOpposite}, optionState{us: qWantYesEmpty}, nil},
	}

	for i, test := range tests {
		eventReceived = nil
		actual := test.start
		actual.opt = Echo
		expected := test.end
		expected.opt = Echo
		test.fn(&actual)
		require.Equal(t, expected, actual, i)
		if data, ok := test.ev.([]byte); ok {
			data = append(data, Echo)
			require.Equal(t, data, eventReceived, i)
		} else {
			require.Nil(t, eventReceived, i)
		}
	}
}

func TestOptionEnabled(t *testing.T) {
	enabledForThem := func(os optionState) bool { return os.EnabledForThem() }
	enabledForUs := func(os optionState) bool { return os.EnabledForUs() }
	var tests = []struct {
		enabled  func(optionState) bool
		state    optionState
		expected bool
	}{
		{enabledForThem, optionState{them: qNo}, false},
		{enabledForThem, optionState{them: qYes}, true},
		{enabledForThem, optionState{them: qWantNoEmpty}, false},
		{enabledForThem, optionState{them: qWantNoOpposite}, false},
		{enabledForThem, optionState{them: qWantYesEmpty}, false},
		{enabledForThem, optionState{them: qWantYesOpposite}, false},

		{enabledForUs, optionState{us: qNo}, false},
		{enabledForUs, optionState{us: qYes}, true},
		{enabledForUs, optionState{us: qWantNoEmpty}, false},
		{enabledForUs, optionState{us: qWantNoOpposite}, false},
		{enabledForUs, optionState{us: qWantYesEmpty}, false},
		{enabledForUs, optionState{us: qWantYesOpposite}, false},
	}

	for i, test := range tests {
		actual := test.enabled(test.state)
		require.Equal(t, test.expected, actual, i)
	}
}

func TestOptionMapHandleNegotiation(t *testing.T) {
	var actual []byte
	d := event.NewDispatcher()
	d.ListenFunc(eventSend, func(data any) error {
		actual = data.([]byte)
		return nil
	})
	m := NewOptionMap(d)
	m.Get(Echo).Allow(true, true)
	var tests = []struct {
		data     negotiation
		expected []byte
	}{
		{negotiation{DO, Echo}, []byte{IAC, WILL, Echo}},
		{negotiation{WILL, Echo}, []byte{IAC, DO, Echo}},
		{negotiation{DO, SuppressGoAhead}, []byte{IAC, WONT, SuppressGoAhead}},
		{negotiation{WILL, SuppressGoAhead}, []byte{IAC, DONT, SuppressGoAhead}},
	}
	for _, test := range tests {
		actual = nil
		m.handleNegotiation(&test.data)
		require.Equal(t, test.expected, actual)
	}
}

func TestOptionEvent(t *testing.T) {
	var actual OptionData
	d := event.NewDispatcher()
	d.ListenFunc(EventOption, func(data any) error {
		actual = data.(OptionData)
		return nil
	})
	var tests = []struct {
		state    optionState
		cmd      byte
		expected OptionData
	}{
		{optionState{allowThem: true, them: qNo}, WILL, OptionData{&optionState{allowThem: true, them: qYes}, true, false}},
		{optionState{allowThem: true, them: qWantNoOpposite}, WILL, OptionData{&optionState{allowThem: true, them: qYes}, true, false}},
		{optionState{allowThem: true, them: qWantYesEmpty}, WILL, OptionData{&optionState{allowThem: true, them: qYes}, true, false}},
		{optionState{allowUs: true, us: qNo}, DO, OptionData{&optionState{allowUs: true, us: qYes}, false, true}},
		{optionState{allowUs: true, us: qWantNoOpposite}, DO, OptionData{&optionState{allowUs: true, us: qYes}, false, true}},
		{optionState{allowUs: true, us: qWantYesEmpty}, DO, OptionData{&optionState{allowUs: true, us: qYes}, false, true}},
		{optionState{them: qYes}, WONT, OptionData{&optionState{them: qNo}, true, false}},
		{optionState{us: qYes}, DONT, OptionData{&optionState{us: qNo}, false, true}},
	}
	for _, test := range tests {
		actual = OptionData{}
		state := test.state
		state.receive(d, test.cmd)
		require.Equal(t, test.expected, actual)
	}
}
