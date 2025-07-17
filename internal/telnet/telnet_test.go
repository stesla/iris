package telnet

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type mockConn struct {
	rbuf bytes.Buffer
	wbuf bytes.Buffer
}

func (m *mockConn) Close() error                       { return nil }
func (m *mockConn) LocalAddr() net.Addr                { return nil }
func (m *mockConn) Read(b []byte) (n int, err error)   { return m.rbuf.Read(b) }
func (m *mockConn) RemoteAddr() net.Addr               { return nil }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }
func (m *mockConn) Write(b []byte) (n int, err error)  { return m.wbuf.Write(b) }

// TODO: test Read contract parameters

func TestReadSimple(t *testing.T) {
	var tests = []struct {
		val, expected []byte
	}{
		{[]byte("foo"), []byte("foo")},
		{[]byte{'h', IAC, NOP, 'i'}, []byte("hi")},
		{[]byte{'h', IAC, IAC, 'i'}, []byte{'h', IAC, 'i'}},
		{[]byte("foo\r\nbar"), []byte("foo\nbar")},
		{[]byte("foo\r\x00bar"), []byte("foo\rbar")},
		{[]byte{'h', IAC, SB, Echo, IAC, SE, 'i'}, []byte("hi")},
		{
			func() (result []byte) {
				for c := range byte(127) {
					result = append(result, '\r', c)
				}
				return
			}(),
			[]byte("\r\n"),
		},
	}
	for _, test := range tests {
		tcp := &mockConn{}
		tcp.rbuf.Write(test.val)
		telnet := Wrap(tcp)
		actual := make([]byte, len(test.val))
		n, err := telnet.Read(actual)
		require.NoError(t, err)
		require.Equal(t, test.expected, actual[:n])
	}
}

func TestSplitInput(t *testing.T) {
	var tests = []struct {
		vals     [][]byte
		expected []byte
	}{
		{[][]byte{{'h', IAC}, {NOP, 'i'}}, []byte("hi")},
		{[][]byte{{'h', IAC}, {IAC, 'i'}}, []byte{'h', IAC, 'i'}},
		{[][]byte{[]byte("foo\r"), []byte("\nbar")}, []byte("foo\nbar")},
		{[][]byte{[]byte("foo\r"), []byte("\x00bar")}, []byte("foo\rbar")},
		{[][]byte{{'h', IAC, SB}, {Echo, IAC}, {SE, 'i'}}, []byte("hi")},
	}
	const bufsize = 16
	for _, test := range tests {
		tcp := &mockConn{}
		telnet := Wrap(tcp)
		buf := make([]byte, bufsize)
		n := 0
		for _, val := range test.vals {
			tcp.rbuf.Write(val)
			nv, err := telnet.Read(buf[n:])
			require.NoError(t, err)
			n += nv
		}
		require.Equal(t, test.expected, buf[:n])
	}
}

func TestWriteSimple(t *testing.T) {
	var tests = []struct {
		val, expected []byte
	}{
		{[]byte("foo"), []byte("foo")},
		{[]byte{'h', IAC, 'i'}, []byte{'h', IAC, IAC, 'i'}},
		{[]byte("foo\nbar"), []byte("foo\r\nbar")},
		{[]byte("foo\rbar"), []byte("foo\r\x00bar")},
	}
	for _, test := range tests {
		tcp := &mockConn{}
		telnet := Wrap(tcp)
		n, err := telnet.Write(test.val)
		require.NoError(t, err)
		require.Equal(t, len(test.val), n)
		require.Equal(t, test.expected, tcp.wbuf.Bytes())
	}
}
