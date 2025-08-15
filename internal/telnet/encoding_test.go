package telnet

import (
	"bytes"
	"context"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stesla/iris/internal/event"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	unicoding "golang.org/x/text/encoding/unicode"
)

func TestDefaultEncodingASCII(t *testing.T) {
	var output bytes.Buffer
	tcp := &mockConn{Reader: bytes.NewBuffer([]byte{IAC, IAC, 128, 129}), Writer: &output}
	telnet := Wrap(context.Background(), tcp)

	expected := make([]byte, 9)
	utf8.EncodeRune(expected, unicode.ReplacementChar)
	utf8.EncodeRune(expected[3:], unicode.ReplacementChar)
	utf8.EncodeRune(expected[6:], unicode.ReplacementChar)

	buf := make([]byte, bufsize)
	n, err := telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, expected, buf[:n])

	n, err = telnet.Write([]byte{IAC, 128, 129})
	require.ErrorContains(t, err, "rune not supported")
	require.Equal(t, 0, n)
}

func TestTransmitBinary(t *testing.T) {
	var output bytes.Buffer
	tcp := &mockConn{Writer: &output}
	telnet := Wrap(context.Background(), tcp)

	handler := &TransmitBinaryHandler{}
	telnet.RegisterHandler(handler)
	dispatch(telnet.Context(), event.Event{
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

	dispatch(telnet.Context(), event.Event{
		Name: EventOption,
		Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qNo, us: qNo}, ChangedThem: true, ChangedUs: true},
	})
	dispatch(telnet.Context(), event.Event{
		Name: EventOption,
		Data: OptionData{OptionState: &optionState{opt: SuppressGoAhead, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true},
	})
	tcp.Reader = bytes.NewReader([]byte{128, 129, 255, 255})
	output.Reset()

	expected := make([]byte, 9)
	utf8.EncodeRune(expected, unicode.ReplacementChar)
	utf8.EncodeRune(expected[3:], unicode.ReplacementChar)
	utf8.EncodeRune(expected[6:], unicode.ReplacementChar)

	n, err = telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, expected, buf[:n])

	n, err = telnet.Write([]byte{IAC, 254, 253})
	require.ErrorContains(t, err, "rune not supported")
	require.Equal(t, 0, n)

	dispatch(telnet.Context(), event.Event{
		Name: EventOption,
		Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true},
	})

	handler.Unregister()

	tcp.Reader = bytes.NewReader([]byte{128, 129, 255, 255})
	n, err = telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, expected, buf[:n])

	output.Reset()
	n, err = telnet.Write([]byte{IAC, 254, 253})
	require.ErrorContains(t, err, "rune not supported")
	require.Equal(t, 0, n)
}

func (h *CharsetHandler) reset() {
	*h = CharsetHandler{ctx: h.ctx}
}

func TestCharsetSubnegotiation(t *testing.T) {
	options := NewOptionMap()
	options.set(&optionState{opt: Charset, them: qYes, us: qYes})
	dispatcher := event.NewDispatcher()
	dispatcher.Listen(EventNegotation, options)
	ctx := context.Background()
	ctx = context.WithValue(ctx, KeyDispatcher, dispatcher)
	ctx = context.WithValue(ctx, KeyOptionMap, options)

	var charset CharsetHandler
	charset.Register(ctx)

	var bytesSent []byte

	dispatcher.ListenFunc(EventSend, func(_ context.Context, ev event.Event) error {
		bytesSent = ev.Data.([]byte)
		return nil
	})

	var capturedEvent *event.Event
	captureEvent := func(_ context.Context, ev event.Event) error {
		capturedEvent = &ev
		return nil
	}
	dispatcher.ListenFunc(EventCharsetAccepted, captureEvent)
	dispatcher.ListenFunc(EventCharsetRejected, captureEvent)

	tests := []struct {
		data     []byte
		expected []byte
		event    any
		init     func()
		assert   func()
	}{
		{
			data:     []byte{CharsetRequest},
			expected: []byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
		},
		{
			data:     append([]byte{CharsetRequest}, ';'),
			expected: []byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
		},
		{
			data:     append([]byte{CharsetRequest}, "[TTABLE]\x01"...),
			expected: []byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
		},
		{
			data:     append([]byte{CharsetRequest}, "[TTABLE]\x01;"...),
			expected: []byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
		},
		{
			data:     append([]byte{CharsetRequest}, ";BOGUS;ENCODING;NAMES"...),
			expected: []byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
		},
		{
			data:     append([]byte{CharsetRequest}, ";US-ASCII;BOGUS"...),
			expected: []byte{IAC, SB, Charset, CharsetAccepted, 'U', 'S', '-', 'A', 'S', 'C', 'I', 'I', IAC, SE},
			event:    event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: ASCII}},
		},
		{
			data:     append([]byte{CharsetRequest}, ";UTF-8;US-ASCII"...),
			expected: []byte{IAC, SB, Charset, CharsetAccepted, 'U', 'T', 'F', '-', '8', IAC, SE},
			event:    event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicoding.UTF8}},
		},
		{
			data:     append([]byte{CharsetRequest}, "[TTABLE]\x01;UTF-8;US-ASCII"...),
			expected: []byte{IAC, SB, Charset, CharsetAccepted, 'U', 'T', 'F', '-', '8', IAC, SE},
			event:    event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicoding.UTF8}},
		},
		{
			init: func() {
				charset.RequestEncoding(unicoding.UTF8)
			},
			data:  []byte{CharsetRejected},
			event: event.Event{Name: EventCharsetRejected},
			assert: func() {
				require.Nil(t, charset.requestedEncodings)
			},
		},
		{
			init: func() {
				charset.RequestEncoding(unicoding.UTF8)
			},
			data:  append([]byte{CharsetAccepted}, "ISO-8859-1"...),
			event: event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: charmap.ISO8859_1}},
			assert: func() {
				require.Nil(t, charset.requestedEncodings)
			},
		},
		{
			data:     []byte{CharsetTTableIs, 1, ';'},
			expected: []byte{IAC, SB, Charset, CharsetTTableRejected, IAC, SE},
		},
		{
			init: func() {
				charset.IsServer = true
				charset.RequestEncoding(unicoding.UTF8)
			},
			data:     append([]byte{CharsetRequest}, ";UTF-8;US-ASCII"...),
			expected: []byte{IAC, SB, Charset, CharsetRejected, IAC, SE},
		},
		{
			init: func() {
				charset.RequestEncoding(unicoding.UTF8)
			},
			data:     append([]byte{CharsetRequest}, ";UTF-8;US-ASCII"...),
			expected: []byte{IAC, SB, Charset, CharsetAccepted, 'U', 'T', 'F', '-', '8', IAC, SE},
			event:    event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicoding.UTF8}},
			assert: func() {
				require.Nil(t, charset.requestedEncodings)
			},
		},
	}

	for i, test := range tests {
		charset.reset()
		if test.init != nil {
			test.init()
		}
		bytesSent, capturedEvent = nil, nil
		err := dispatch(ctx, event.Event{Name: EventSubnegotiation, Data: Subnegotiation{
			Opt:  Charset,
			Data: test.data,
		}})
		require.NoError(t, err)
		require.Equal(t, test.expected, bytesSent)
		if test.event == nil {
			require.Nil(t, capturedEvent, i)
		} else {
			require.Equal(t, test.event, *capturedEvent)
		}
		if test.assert != nil {
			test.assert()
		}
	}
}

type mockEncodable struct {
	t                 *testing.T
	readEnc, writeEnc encoding.Encoding
}

func (m *mockEncodable) SetReadEncoding(enc encoding.Encoding) {
	require.NotNil(m.t, enc)
	m.readEnc = enc
}

func (m *mockEncodable) SetWriteEncoding(enc encoding.Encoding) {
	require.NotNil(m.t, enc)
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
	encodable := &mockEncodable{t: t}
	ctx := context.Background()
	ctx = context.WithValue(ctx, KeyDispatcher, dispatcher)
	ctx = context.WithValue(ctx, KeyOptionMap, options)
	ctx = context.WithValue(ctx, KeyEncodable, encodable)
	tests := []struct {
		events                            []event.Event
		expectedReadEnc, expectedWriteEnc encoding.Encoding
	}{
		{[]event.Event{
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicoding.UTF8}},
		}, nil, nil},
		{[]event.Event{
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicoding.UTF8}},
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
		}, unicoding.UTF8, unicoding.UTF8},
		{[]event.Event{
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicoding.UTF8}},
		}, unicoding.UTF8, unicoding.UTF8},
		{[]event.Event{
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicoding.UTF8}},
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qNo}, ChangedThem: false, ChangedUs: true}},
		}, ASCII, ASCII},
		{[]event.Event{
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qYes, us: qYes}, ChangedThem: true, ChangedUs: true}},
			{Name: EventCharsetAccepted, Data: CharsetData{Encoding: unicoding.UTF8}},
			{Name: EventOption, Data: OptionData{OptionState: &optionState{opt: TransmitBinary, them: qNo, us: qYes}, ChangedThem: true, ChangedUs: false}},
		}, ASCII, ASCII},
	}
	for _, test := range tests {
		options.set(&optionState{opt: TransmitBinary})
		h := &CharsetHandler{}
		*encodable = mockEncodable{t: t}
		h.Register(ctx)
		for _, event := range test.events {
			err := dispatch(ctx, event)
			require.NoError(t, err)
		}
		require.Equal(t, test.expectedReadEnc, encodable.readEnc)
		require.Equal(t, test.expectedWriteEnc, encodable.writeEnc)
	}
}

func TestCharsetRequestEncoding(t *testing.T) {
	var sentData []byte
	dispatcher := event.NewDispatcher()
	dispatcher.ListenFunc(EventSend, func(_ context.Context, ev event.Event) error {
		sentData = ev.Data.([]byte)
		return nil
	})
	options := NewOptionMap()
	options.set(&optionState{opt: Charset, them: qYes, us: qYes})
	ctx := context.Background()
	ctx = context.WithValue(ctx, KeyDispatcher, dispatcher)
	ctx = context.WithValue(ctx, KeyOptionMap, options)

	handler := &CharsetHandler{}
	handler.Register(ctx)
	err := handler.RequestEncoding(unicoding.UTF8, charmap.ISO8859_1, charmap.Windows1252, ASCII)
	require.NoError(t, err)
	expected := []byte{IAC, SB, Charset, CharsetRequest}
	expected = append(expected, ";UTF-8"...)
	expected = append(expected, ";ISO_8859-1:1987"...)
	expected = append(expected, ";windows-1252"...)
	expected = append(expected, ";US-ASCII"...)
	expected = append(expected, IAC, SE)
	require.Equal(t, expected, sentData)
}
