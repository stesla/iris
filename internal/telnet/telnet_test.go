package telnet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stesla/iris/internal/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/unicode"
)

type mockConn struct {
	io.Reader
	io.Writer
}

func (m *mockConn) Close() error                       { return nil }
func (m *mockConn) LocalAddr() net.Addr                { return nil }
func (m *mockConn) RemoteAddr() net.Addr               { return nil }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }

const bufsize = 16

func TestReadIntoEmptySlice(t *testing.T) {
	var in bytes.Buffer
	telnet := Wrap(context.Background(), &mockConn{Reader: &in})
	buf := []byte{}
	n, err := telnet.Read(buf)
	require.Equal(t, 0, n)
	require.ErrorIs(t, err, io.EOF)
}

func TestRead(t *testing.T) {
	var tests = []struct {
		vals     [][]byte
		expected []byte
	}{
		{[][]byte{[]byte("foo")}, []byte("foo")},
		{[][]byte{{'h', IAC}, {NOP, 'a'}}, []byte("ha")},
		{[][]byte{{'h', IAC}, {IAC, 'e'}}, []byte{'h', IAC, 'e'}},
		{[][]byte{[]byte("foo\r"), []byte("\nbar")}, []byte("foo\nbar")},
		{[][]byte{[]byte("foo\r"), []byte("\x00bar")}, []byte("foo\rbar")},
		{[][]byte{{'h', IAC, SB, Echo, IAC, SE, 'i'}}, []byte("hi")},
		{
			func() [][]byte {
				var bytes []byte
				for c := range byte(127) {
					bytes = append(bytes, '\r', c)
				}
				return [][]byte{bytes}
			}(),
			[]byte("\r\n"),
		},
	}
	for _, test := range tests {
		tcp := &mockConn{}
		telnet := Wrap(context.Background(), tcp)
		telnet.SetReadEncoding(encoding.Nop)
		buf := make([]byte, bufsize)
		n := 0
		for _, val := range test.vals {
			tcp.Reader = bytes.NewReader(val)
			nv, err := telnet.Read(buf[n:])
			require.NoError(t, err, test.expected)
			n += nv
		}
		require.Equal(t, test.expected, buf[:n])
	}
}

type boomReader struct {
	n   int
	err error
}

func (r boomReader) Read(b []byte) (n int, err error) {
	for i := 0; i < r.n && i < len(b); i++ {
		b[i] = 'A' + byte(i)
	}
	return r.n, r.err
}

func TestReadWithUnderlyingError(t *testing.T) {
	tcp := &mockConn{Reader: boomReader{3, errors.New("boom")}}
	telnet := Wrap(context.Background(), tcp)
	buf := make([]byte, bufsize)
	n, err := telnet.Read(buf)
	require.Error(t, err, "boom")
	require.Equal(t, 3, n)
	require.Equal(t, "ABC", string(buf[:n]))
}

func TestEOFWaitsForNextRead(t *testing.T) {
	tcp := &mockConn{Reader: boomReader{3, io.EOF}}
	telnet := Wrap(context.Background(), tcp)
	buf := make([]byte, bufsize)
	n, err := telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.Equal(t, "ABC", string(buf[:n]))
	n, err = telnet.Read(buf[n:])
	require.Equal(t, io.EOF, err)
	require.Equal(t, 0, n)
}

func TestWrite(t *testing.T) {
	var tests = []struct {
		val, expected []byte
	}{
		{[]byte("foo"), []byte("foo")},
		{[]byte{'h', IAC, 'i'}, []byte{'h', IAC, IAC, 'i'}},
		{[]byte("foo\nbar"), []byte("foo\r\nbar")},
		{[]byte("foo\rbar"), []byte("foo\r\x00bar")},
	}
	for _, test := range tests {
		var buf bytes.Buffer
		tcp := &mockConn{Writer: &buf}
		telnet := Wrap(context.Background(), tcp)
		telnet.SetWriteEncoding(encoding.Nop)
		n, err := telnet.Write(test.val)
		require.NoError(t, err)
		require.Equal(t, len(test.val), n)
		require.Equal(t, test.expected, buf.Bytes())
	}
}

func TestReadCommand(t *testing.T) {
	var tests = []struct {
		val, expected []byte
		event         any
	}{
		{[]byte{'a', IAC, GA, 'a'}, []byte("aa"), "go ahead"},
		{[]byte{'a', IAC, EOR, 'a'}, []byte("aa"), "end of record"},
		{[]byte{'b', IAC, DO, Echo, 'b'}, []byte("bb"), Negotiation{Cmd: DO, Opt: Echo}},
		{[]byte{'c', IAC, DONT, Echo, 'c'}, []byte("cc"), Negotiation{Cmd: DONT, Opt: Echo}},
		{[]byte{'d', IAC, WILL, Echo, 'd'}, []byte("dd"), Negotiation{Cmd: WILL, Opt: Echo}},
		{[]byte{'e', IAC, WONT, Echo, 'e'}, []byte("ee"), Negotiation{Cmd: WONT, Opt: Echo}},
		{[]byte{'f', IAC, SB, Echo, 'f', 'o', 'o', IAC, SE, 'f'}, []byte("ff"), Subnegotiation{Opt: Echo, Data: []byte("foo")}},
		{[]byte{'g', IAC, SB, Echo, IAC, IAC, IAC, SE, 'g'}, []byte("gg"), Subnegotiation{Opt: Echo, Data: []byte{IAC}}},
		{[]byte{'h', IAC, SB, IAC, SE, 'h'}, []byte("hh"), nil},
		{[]byte{'i', IAC, SB, Echo, IAC, SE, 'i'}, []byte("ii"), Subnegotiation{Opt: Echo, Data: []byte{}}},
	}
	for _, test := range tests {
		var capturedEvent any
		captureEvent := func(_ context.Context, ev event.Event) error {
			capturedEvent = ev.Data
			return nil
		}
		tcp := &mockConn{Reader: bytes.NewReader(test.val), Writer: io.Discard}
		telnet := wrap(context.Background(), tcp)
		telnet.Dispatcher.ListenFunc(EventEndOfRecord, func(context.Context, event.Event) error {
			capturedEvent = "end of record"
			return nil
		})
		telnet.Dispatcher.ListenFunc(EventGoAhead, func(context.Context, event.Event) error {
			capturedEvent = "go ahead"
			return nil
		})
		telnet.Dispatcher.ListenFunc(EventNegotation, captureEvent)
		telnet.Dispatcher.ListenFunc(EventSubnegotiation, captureEvent)
		buf := make([]byte, bufsize)
		n, err := telnet.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, test.expected, buf[:n])
		assert.Equal(t, test.event, capturedEvent)
	}
}

func TestSuppressGoAhead(t *testing.T) {
	var output bytes.Buffer
	tcp := &mockConn{Writer: &output}
	telnet := wrap(context.Background(), tcp)
	telnet.options.set(&optionState{opt: SuppressGoAhead, us: qYes})
	_, err := telnet.Write([]byte("xyzzy"))
	require.NoError(t, err)
	err = telnet.SendGoAhead()
	require.NoError(t, err)
	require.Equal(t, []byte("xyzzy"), output.Bytes())

	telnet.options.set(&optionState{opt: SuppressGoAhead, us: qNo})
	output.Reset()
	_, err = telnet.Write([]byte("xyzzy"))
	require.NoError(t, err)
	err = telnet.SendGoAhead()
	require.NoError(t, err)
	require.Equal(t, append([]byte("xyzzy"), IAC, GA), output.Bytes())

	telnet.SuppressGoAhead(true)
	output.Reset()
	_, err = telnet.Write([]byte("xyzzy"))
	require.NoError(t, err)
	err = telnet.SendGoAhead()
	require.NoError(t, err)
	require.Equal(t, []byte("xyzzy"), output.Bytes())
}

func TestEndOfRecord(t *testing.T) {
	var output bytes.Buffer
	tcp := &mockConn{Writer: &output}
	telnet := wrap(context.Background(), tcp)
	telnet.options.set(&optionState{opt: EndOfRecord, us: qYes})
	telnet.options.set(&optionState{opt: SuppressGoAhead, us: qYes})
	_, err := telnet.Write([]byte("foo"))
	require.NoError(t, err)
	telnet.SendEndOfRecord()
	require.Equal(t, []byte{'f', 'o', 'o', IAC, EOR}, output.Bytes())
}

func TestLargeWrite(t *testing.T) {
	var output bytes.Buffer
	tcp := &mockConn{Writer: &output}
	telnet := wrap(context.Background(), tcp)
	data := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz012345689"), 1024)
	n, err := telnet.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)
	require.Equal(t, data, output.Bytes())
}

// lockedBuffer collects writes, so a test sees races in the code it is testing
// rather than in the buffer.
type lockedBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}

func TestConcurrentWrites(t *testing.T) {
	const writers, lines = 4, 50
	var output lockedBuffer
	telnet := Wrap(context.Background(), &mockConn{Writer: &output})

	// longer than the encoder's buffer, so each line takes several writes to
	// the connection
	line := func(w, i int) string {
		return fmt.Sprintf("%d-%d-%s", w, i, strings.Repeat("x", 10000))
	}
	var want []string
	var wg sync.WaitGroup
	for w := range writers {
		for i := range lines {
			want = append(want, line(w, i))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range lines {
				_, err := telnet.Write([]byte(line(w, i) + "\n"))
				assert.NoError(t, err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range lines {
			telnet.SetWriteEncoding(unicode.UTF8)
			telnet.SetWriteEncoding(ASCII)
		}
	}()
	wg.Wait()

	got := strings.Split(strings.TrimSuffix(output.String(), "\r\n"), "\r\n")
	assert.ElementsMatch(t, want, got)
}

func TestCloseUnblocksStuckWrite(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	telnet := Wrap(context.Background(), server)

	done := make(chan error, 1)
	go func() {
		_, err := telnet.Write([]byte("nobody is reading\n"))
		done <- err
	}()
	// a second write waits on the first, which is stuck on the pipe
	second := make(chan error, 1)
	go func() {
		_, err := telnet.Write([]byte("nor this\n"))
		second <- err
	}()
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, telnet.Close())

	for _, ch := range []chan error{done, second} {
		select {
		case err := <-ch:
			assert.Error(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("Close didn't unblock a stuck write")
		}
	}
}
