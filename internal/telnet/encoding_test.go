package telnet

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stesla/iris/internal/event"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

func TestDefaultEncodingASCII(t *testing.T) {
	var output bytes.Buffer
	tcp := &mockConn{Reader: bytes.NewBuffer([]byte{IAC, IAC, 128, 129}), Writer: &output}
	telnet := Wrap(tcp)

	buf := make([]byte, bufsize)
	n, err := telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, buf[:n])

	n, err = telnet.Write([]byte{IAC, 128, 129})
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, output.Bytes()[:n])
}

func TestTransmitBinary(t *testing.T) {
	var output bytes.Buffer
	tcp := &mockConn{Writer: &output}
	telnet := Wrap(tcp)

	unregister := telnet.RegisterHandler(&TransmitBinaryHandler{})
	Dispatch(telnet.Context(), event.Event{
		Name: EventOption,
		Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true},
	})
	tcp.Reader = bytes.NewReader([]byte{128, 129, 255, 255})
	output.Reset()

	buf := make([]byte, bufsize)
	n, err := telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{128, 129, 255}, buf[:n])

	n, err = telnet.Write([]byte{IAC, 254, 253})
	require.NoError(t, err)
	require.Equal(t, []byte{IAC, IAC, 254, 253}, output.Bytes()[:n+1])

	Dispatch(telnet.Context(), event.Event{
		Name: EventOption,
		Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qNo, us: qNo}, ChangedThem: true, ChangedUs: true},
	})
	Dispatch(telnet.Context(), event.Event{
		Name: EventOption,
		Data: OptionData{OptionState: &optionState{opt: SuppressGoAhead, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true},
	})
	tcp.Reader = bytes.NewReader([]byte{128, 129, 255, 255})
	output.Reset()

	n, err = telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, buf[:n])

	n, err = telnet.Write([]byte{IAC, 254, 253})
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, output.Bytes()[:n])

	Dispatch(telnet.Context(), event.Event{
		Name: EventOption,
		Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true},
	})

	unregister()

	tcp.Reader = bytes.NewReader([]byte{128, 129, 255, 255})
	n, err = telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, buf[:n])

	output.Reset()
	n, err = telnet.Write([]byte{IAC, 254, 253})
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, output.Bytes()[:n])
}

func TestCharsetSubnegotiation(t *testing.T) {
	tcp := &mockConn{Writer: io.Discard}
	telnet := Wrap(tcp)

	charset := &CharsetHandler{}
	telnet.RegisterHandler(charset)

	Dispatch(telnet.Context(), event.Event{Name: eventNegotation, Data: negotiation{DO, Charset}})
	Dispatch(telnet.Context(), event.Event{Name: eventNegotation, Data: negotiation{WILL, Charset}})

	var bytesSent []byte

	telnet.ListenFunc(eventSend, func(_ context.Context, ev event.Event) error {
		bytesSent = ev.Data.([]byte)
		return nil
	})

	var capturedEvent *event.Event
	captureEvent := func(_ context.Context, ev event.Event) error {
		capturedEvent = &ev
		return nil
	}
	telnet.ListenFunc(EventCharsetAccepted, captureEvent)
	telnet.ListenFunc(EventCharsetRejected, captureEvent)

	tests := []struct {
		data     []byte
		expected []byte
		event    any
	}{
		{
			[]byte{CharsetRequest},
			[]byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
			nil,
		},
		{
			append([]byte{CharsetRequest}, ';'),
			[]byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
			nil,
		},
		{
			append([]byte{CharsetRequest}, "[TTABLE]\x01"...),
			[]byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
			nil,
		},
		{
			append([]byte{CharsetRequest}, "[TTABLE]\x01;"...),
			[]byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
			nil,
		},
		{
			append([]byte{CharsetRequest}, ";BOGUS;ENCODING;NAMES"...),
			[]byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
			nil,
		},
		{
			append([]byte{CharsetRequest}, ";US-ASCII;BOGUS"...),
			[]byte{IAC, SB, Charset, CharsetAccepted, 'U', 'S', '-', 'A', 'S', 'C', 'I', 'I', IAC, SE},
			event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: ASCII}},
		},
		{
			append([]byte{CharsetRequest}, ";UTF-8;US-ASCII"...),
			[]byte{IAC, SB, Charset, CharsetAccepted, 'U', 'T', 'F', '-', '8', IAC, SE},
			event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicode.UTF8}},
		},
		{
			append([]byte{CharsetRequest}, "[TTABLE]\x01;UTF-8;US-ASCII"...),
			[]byte{IAC, SB, Charset, CharsetAccepted, 'U', 'T', 'F', '-', '8', IAC, SE},
			event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicode.UTF8}},
		},
		{
			[]byte{CharsetRejected},
			nil,
			event.Event{Name: EventCharsetRejected},
		},
		{
			append([]byte{CharsetAccepted}, "ISO-8859-1"...),
			nil,
			event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: charmap.ISO8859_1}},
		},
		{
			[]byte{CharsetTTableIs, 1, ';'},
			[]byte{IAC, SB, Charset, CharsetTTableRejected, IAC, SE},
			nil,
		},
	}

	for _, test := range tests {
		bytesSent, capturedEvent = nil, nil
		err := Dispatch(telnet.Context(), event.Event{Name: eventSubnegotiation, Data: subnegotiation{
			opt:  Charset,
			data: test.data,
		}})
		require.NoError(t, err)
		require.Equal(t, test.expected, bytesSent)
		if test.event == nil {
			require.Nil(t, capturedEvent)
		} else {
			require.Equal(t, test.event, *capturedEvent)
		}
	}
}

type mockEncodable struct {
	readEnc, writeEnc encoding.Encoding
}

func (m *mockEncodable) SetReadEncoding(enc encoding.Encoding) {
	m.readEnc = enc
}

func (m *mockEncodable) SetWriteEncoding(enc encoding.Encoding) {
	m.writeEnc = enc
}

func TestCharsetSetsEncoding(t *testing.T) {
	dispatcher := event.NewDispatcher()
	options := NewOptionMap()
	options.set(&optionState{opt: Charset, them: qYes, us: qYes})
	dispatcher.ListenFunc(EventOption, func(_ context.Context, ev event.Event) error {
		switch opt := ev.Data.(type) {
		case OptionData:
			options.set(opt.OptionState)
		}
		return nil
	})
	encodable := &mockEncodable{}
	ctx := context.Background()
	ctx = context.WithValue(ctx, KeyDispatcher, dispatcher)
	ctx = context.WithValue(ctx, KeyOptionMap, options)
	ctx = context.WithValue(ctx, KeyEncodable, encodable)
	tests := []struct {
		events                            []event.Event
		expectedReadEnc, expectedWriteEnc encoding.Encoding
	}{
		{[]event.Event{
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicode.UTF8}},
		}, nil, nil},
		{[]event.Event{
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicode.UTF8}},
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
		}, unicode.UTF8, unicode.UTF8},
		{[]event.Event{
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicode.UTF8}},
		}, unicode.UTF8, unicode.UTF8},
		{[]event.Event{
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicode.UTF8}},
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qNo}, ChangedThem: false, ChangedUs: true}},
		}, ASCII, ASCII},
		{[]event.Event{
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicode.UTF8}},
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qNo, us: qYes}, ChangedThem: true, ChangedUs: false}},
		}, ASCII, ASCII},
	}
	for _, test := range tests {
		options.set(&optionState{opt: TransmitBinary})
		h := &CharsetHandler{}
		*encodable = mockEncodable{}
		h.Register(ctx)
		for _, event := range test.events {
			Dispatch(ctx, event)
		}
		require.Equal(t, test.expectedReadEnc, encodable.readEnc)
		require.Equal(t, test.expectedWriteEnc, encodable.writeEnc)
	}
}
