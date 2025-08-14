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
	tcp := &mockConn{Writer: io.Discard}
	telnet := Wrap(tcp)

	unregister := telnet.RegisterHandler(&TransmitBinaryHandler{})
	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{DO, TransmitBinary})
	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{WILL, TransmitBinary})
	tcp.Reader = bytes.NewReader([]byte{128, 129, 255, 255})
	tcp.Writer = &output

	buf := make([]byte, bufsize)
	n, err := telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{128, 129, 255}, buf[:n])

	n, err = telnet.Write([]byte{IAC, 254, 253})
	require.NoError(t, err)
	require.Equal(t, []byte{IAC, IAC, 254, 253}, output.Bytes()[:n+1])

	telnet.Get(SuppressGoAhead).Allow(true, true)
	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{DONT, TransmitBinary})
	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{WONT, TransmitBinary})
	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{DO, SuppressGoAhead})
	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{WILL, SuppressGoAhead})
	tcp.Reader = bytes.NewReader([]byte{128, 129, 255, 255})
	output.Reset()

	n, err = telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, buf[:n])

	n, err = telnet.Write([]byte{IAC, 254, 253})
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, output.Bytes()[:n])

	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{DO, TransmitBinary})
	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{WILL, TransmitBinary})

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

	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{DO, Charset})
	telnet.Dispatch(telnet.Context(), eventNegotation, &negotiation{WILL, Charset})

	var bytesSent []byte

	telnet.ListenFunc(eventSend, func(_ context.Context, data any) error {
		bytesSent = data.([]byte)
		return nil
	})

	var event any

	telnet.ListenFunc(EventCharsetAccepted, func(_ context.Context, data any) error {
		event = data
		return nil
	})

	telnet.ListenFunc(EventCharsetRejected, func(context.Context, any) error {
		event = EventCharsetRejected
		return nil
	})

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
			CharsetData{Encoding: ASCII},
		},
		{
			append([]byte{CharsetRequest}, ";UTF-8;US-ASCII"...),
			[]byte{IAC, SB, Charset, CharsetAccepted, 'U', 'T', 'F', '-', '8', IAC, SE},
			CharsetData{Encoding: unicode.UTF8},
		},
		{
			append([]byte{CharsetRequest}, "[TTABLE]\x01;UTF-8;US-ASCII"...),
			[]byte{IAC, SB, Charset, CharsetAccepted, 'U', 'T', 'F', '-', '8', IAC, SE},
			CharsetData{Encoding: unicode.UTF8},
		},
		{
			[]byte{CharsetRejected},
			nil,
			EventCharsetRejected,
		},
		{
			append([]byte{CharsetAccepted}, "ISO-8859-1"...),
			nil,
			CharsetData{Encoding: charmap.ISO8859_1},
		},
		{
			[]byte{CharsetTTableIs, 1, ';'},
			[]byte{IAC, SB, Charset, CharsetTTableRejected, IAC, SE},
			nil,
		},
	}

	for _, test := range tests {
		bytesSent, event = nil, nil
		err := telnet.Dispatch(telnet.Context(), eventSubnegotiation, &subnegotiation{
			opt:  Charset,
			data: test.data,
		})
		require.NoError(t, err)
		require.Equal(t, test.expected, bytesSent)
		require.Equal(t, test.event, event)
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

type Event struct {
	event.Name
	Data any
}

func TestCharsetSetsEncoding(t *testing.T) {
	dispatcher := event.NewDispatcher()
	options := NewOptionMap()
	options.set(&optionState{opt: Charset, them: qYes, us: qYes})
	dispatcher.ListenFunc(EventOption, func(_ context.Context, data any) error {
		switch opt := data.(type) {
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
		events                            []Event
		expectedReadEnc, expectedWriteEnc encoding.Encoding
	}{
		{[]Event{
			{EventCharsetAccepted, CharsetData{Encoding: unicode.UTF8}},
		}, nil, nil},
		{[]Event{
			{EventCharsetAccepted, CharsetData{Encoding: unicode.UTF8}},
			{EventOption, OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
		}, unicode.UTF8, unicode.UTF8},
		{[]Event{
			{EventOption, OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
			{EventCharsetAccepted, CharsetData{Encoding: unicode.UTF8}},
		}, unicode.UTF8, unicode.UTF8},
		{[]Event{
			{EventOption, OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
			{EventCharsetAccepted, CharsetData{Encoding: unicode.UTF8}},
			{EventOption, OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qNo}, ChangedThem: false, ChangedUs: true}},
		}, ASCII, ASCII},
		{[]Event{
			{EventOption, OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
			{EventCharsetAccepted, CharsetData{Encoding: unicode.UTF8}},
			{EventOption, OptionData{OptionState: &optionState{opt: TransmitBinary, them: qNo, us: qYes}, ChangedThem: true, ChangedUs: false}},
		}, ASCII, ASCII},
	}
	for _, test := range tests {
		options.set(&optionState{opt: TransmitBinary})
		h := &CharsetHandler{}
		*encodable = mockEncodable{}
		h.Register(ctx)
		for _, event := range test.events {
			Dispatch(ctx, event.Name, event.Data)
		}
		require.Equal(t, test.expectedReadEnc, encodable.readEnc)
		require.Equal(t, test.expectedWriteEnc, encodable.writeEnc)
	}
}
