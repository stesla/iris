package telnet

import (
	"io"
	"net"

	"github.com/stesla/iris/internal/event"
)

const Foo = "foo"

type Conn interface {
	net.Conn
	event.Dispatcher
}

type conn struct {
	net.Conn
	event.Dispatcher

	cmd    byte
	ds     decodeState
	eof    bool
	sbdata []byte

	options *OptionMap
}

func Dial(address string) (Conn, error) {
	tcpconn, err := net.Dial("tcp", address)
	return Wrap(tcpconn), err
}

func Wrap(c net.Conn) Conn {
	return wrap(c)
}

func wrap(c net.Conn) *conn {
	eh := event.NewDispatcher()
	options := NewOptionMap(eh)
	cc := &conn{
		Conn:       c,
		Dispatcher: eh,
		options:    options,
	}
	cc.Listen(eventEOF, cc.handleEOF)
	cc.Listen(eventNegotation, options.handleNegotiation)
	cc.Listen(eventSend, cc.handleSend)
	return cc
}

type decodeState int

const (
	decodeByte decodeState = 0 + iota
	decodeCR
	decodeIAC
	decodeSB
	decodeSBIAC
	decodeOptionNegotation
)

func (c *conn) handleEOF(any) error {
	c.eof = true
	return nil
}

func (c *conn) handleSend(data any) error {
	_, err := c.WriteRaw(data.([]byte))
	return err
}

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
			c.cmd = buf[0]
			switch c.cmd {
			case DO, DONT, WILL, WONT:
				c.ds = decodeOptionNegotation
			case GA:
				c.Dispatch(eventGA, nil)
				c.ds = decodeByte
			case SB:
				c.ds = decodeSB
				c.sbdata = nil
			case IAC:
				copy()
				c.ds = decodeByte
			default:
				c.ds = decodeByte
			}
		case decodeOptionNegotation:
			c.Dispatch(eventNegotation, &negotiation{c.cmd, buf[0]})
			c.ds = decodeByte
		case decodeSB:
			switch buf[0] {
			case IAC:
				c.ds = decodeSBIAC
			default:
				c.sbdata = append(c.sbdata, buf[0])
			}
		case decodeSBIAC:
			switch buf[0] {
			case IAC:
				c.sbdata = append(c.sbdata, IAC)
				c.ds = decodeSB
			case SE:
				c.Dispatch(eventSubnegotiation, &subnegotiation{
					opt:  c.sbdata[0],
					data: c.sbdata[1:],
				})
				c.ds = decodeByte
			}
		}
		buf = buf[1:]
	}
	if err == io.EOF {
		c.Dispatch(eventEOF, nil)
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

const eventEOF event.Name = "internal.end-of-file"
const eventGA event.Name = "internal.go-ahead"
const eventSend event.Name = "internal.send-data"
