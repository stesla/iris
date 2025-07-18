package telnet

import (
	"io"
	"net"
)

const Foo = "foo"

type Conn interface {
	net.Conn
}

type conn struct {
	net.Conn

	ds  decodeState
	eof bool
}

func Dial(address string) (Conn, error) {
	tcpconn, err := net.Dial("tcp", address)
	return Wrap(tcpconn), err
}

func Wrap(c net.Conn) Conn {
	return &conn{Conn: c}
}

type decodeState int

const (
	decodeByte decodeState = 0 + iota
	decodeCR
	decodeIAC
	decodeSB
)

func (c *conn) Read(p []byte) (n int, err error) {
	if c.eof {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}

	buf := make([]byte, len(p))
	nr, err := c.Conn.Read(buf)
	buf = buf[:nr]

	copy := func() {
		p[n] = buf[0]
		n++
	}

	for len(buf) > 0 {
		switch c.ds {
		case decodeByte:
			switch buf[0] {
			case IAC:
				c.ds = decodeIAC
			case '\r':
				c.ds = decodeCR
			default:
				copy()
			}
		case decodeCR:
			switch buf[0] {
			case '\x00':
				buf[0] = '\r'
				fallthrough
			case '\n':
				copy()
			}
			c.ds = decodeByte
		case decodeIAC:
			switch buf[0] {
			case SE:
				c.ds = decodeByte
			case SB:
				c.ds = decodeSB
			case IAC:
				copy()
				fallthrough
			default:
				c.ds = decodeByte
			}
		case decodeSB:
			switch buf[0] {
			case IAC:
				c.ds = decodeIAC
			}
		}
		buf = buf[1:]
	}
	if err == io.EOF {
		c.eof = true
		err = nil
	}
	return
}

func (c *conn) Write(p []byte) (n int, err error) {
	buf := make([]byte, 0, 2*len(p))
	for _, c := range p {
		switch c {
		case IAC:
			buf = append(buf, IAC, IAC)
		case '\n':
			buf = append(buf, '\r', '\n')
		case '\r':
			buf = append(buf, '\r', '\x00')
		default:
			buf = append(buf, c)
		}
		n++
	}
	_, err = c.WriteRaw(buf)
	return
}

func (c *conn) WriteRaw(p []byte) (int, error) {
	return c.Conn.Write(p)
}
