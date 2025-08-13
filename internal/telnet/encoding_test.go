package telnet

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding"
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
	telnet := wrap(tcp)

	unregister := telnet.RegisterHandler(&TransmitBinaryHandler{})
	telnet.Dispatch(telnet.ctx, eventNegotation, &negotiation{DO, TransmitBinary})
	telnet.Dispatch(telnet.ctx, eventNegotation, &negotiation{WILL, TransmitBinary})
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
	telnet.Dispatch(telnet.ctx, eventNegotation, &negotiation{DONT, TransmitBinary})
	telnet.Dispatch(telnet.ctx, eventNegotation, &negotiation{WONT, TransmitBinary})
	telnet.Dispatch(telnet.ctx, eventNegotation, &negotiation{DO, SuppressGoAhead})
	telnet.Dispatch(telnet.ctx, eventNegotation, &negotiation{WILL, SuppressGoAhead})
	tcp.Reader = bytes.NewReader([]byte{128, 129, 255, 255})
	output.Reset()

	n, err = telnet.Read(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, buf[:n])

	n, err = telnet.Write([]byte{IAC, 254, 253})
	require.NoError(t, err)
	require.Equal(t, []byte{encoding.ASCIISub, encoding.ASCIISub, encoding.ASCIISub}, output.Bytes()[:n])

	telnet.Dispatch(telnet.ctx, eventNegotation, &negotiation{DO, TransmitBinary})
	telnet.Dispatch(telnet.ctx, eventNegotation, &negotiation{WILL, TransmitBinary})

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
