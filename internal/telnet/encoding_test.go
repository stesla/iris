package telnet

import (
	"bytes"
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
