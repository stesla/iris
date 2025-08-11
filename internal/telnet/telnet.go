package telnet

import (
	"io"
	"net"

	"github.com/stesla/iris/internal/event"
	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

type Conn interface {
	net.Conn
	event.Dispatcher

	SetEncoding(encoding.Encoding)
	SetReadEncoding(encoding.Encoding)
	SetWriteEncoding(encoding.Encoding)
}

type conn struct {
	net.Conn
	event.Dispatcher
	OptionMap

	r, readEncoded  io.Reader
	w, writeEncoded io.Writer
}

func Dial(address string) (Conn, error) {
	tcpconn, err := net.Dial("tcp", address)
	return Wrap(tcpconn), err
}

func Wrap(c net.Conn) Conn {
	return wrap(c)
}

func wrap(c net.Conn) *conn {
	dispatcher := event.NewDispatcher()
	options := NewOptionMap(dispatcher)
	cc := &conn{
		Conn:       c,
		Dispatcher: dispatcher,
		OptionMap:  options,
		r:          &reader{in: c, d: dispatcher},
		w:          &writer{out: c, options: options},
	}
	cc.SetEncoding(ASCII)
	cc.Listen(eventNegotation, cc.handleNegotiation)
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

func (c *conn) handleSend(data any) error {
	_, err := c.Conn.Write(data.([]byte))
	return err
}

func (c *conn) Read(p []byte) (n int, err error) {
	return c.readEncoded.Read(p)
}

func (c *conn) SetEncoding(enc encoding.Encoding) {
	c.SetReadEncoding(enc)
	c.SetWriteEncoding(enc)
}

func (c *conn) SetReadEncoding(enc encoding.Encoding) {
	c.readEncoded = enc.NewDecoder().Reader(c.r)
}

func (c *conn) SetWriteEncoding(enc encoding.Encoding) {
	c.writeEncoded = enc.NewEncoder().Writer(c.w)
}

func (c *conn) Write(p []byte) (n int, err error) {
	return c.writeEncoded.Write(p)
}

type reader struct {
	in io.Reader
	d  event.Dispatcher

	cmd    byte
	ds     decodeState
	eof    bool
	sbdata []byte
}

func (r *reader) Read(p []byte) (n int, err error) {
	if r.eof {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}

	buf := make([]byte, len(p))
	nr, err := r.in.Read(buf)
	buf = buf[:nr]

	copy := func() {
		p[n] = buf[0]
		n++
	}

	for len(buf) > 0 {
		switch r.ds {
		case decodeByte:
			switch buf[0] {
			case IAC:
				r.ds = decodeIAC
			case '\r':
				r.ds = decodeCR
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
			r.ds = decodeByte
		case decodeIAC:
			r.cmd = buf[0]
			switch r.cmd {
			case DO, DONT, WILL, WONT:
				r.ds = decodeOptionNegotation
			case EOR:
				r.d.Dispatch(eventEndOfRecord, nil)
				r.ds = decodeByte
			case GA:
				r.d.Dispatch(eventGoAhead, nil)
				r.ds = decodeByte
			case SB:
				r.ds = decodeSB
				r.sbdata = nil
			case IAC:
				copy()
				r.ds = decodeByte
			default:
				r.ds = decodeByte
			}
		case decodeOptionNegotation:
			r.d.Dispatch(eventNegotation, &negotiation{r.cmd, buf[0]})
			r.ds = decodeByte
		case decodeSB:
			switch buf[0] {
			case IAC:
				r.ds = decodeSBIAC
			default:
				r.sbdata = append(r.sbdata, buf[0])
			}
		case decodeSBIAC:
			switch buf[0] {
			case IAC:
				r.sbdata = append(r.sbdata, IAC)
				r.ds = decodeSB
			case SE:
				r.d.Dispatch(eventSubnegotiation, &subnegotiation{
					opt:  r.sbdata[0],
					data: r.sbdata[1:],
				})
				r.ds = decodeByte
			}
		}
		buf = buf[1:]
	}
	if err == io.EOF {
		r.eof = true
		err = nil
	}
	return
}

type writer struct {
	out     io.Writer
	options OptionMap
}

func (w *writer) Write(p []byte) (n int, err error) {
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
	if w.shouldSendEndOfRecord() {
		buf = append(buf, IAC, EOR)
	}
	if w.shouldSendGoAhead() {
		buf = append(buf, IAC, GA)
	}
	_, err = w.out.Write(buf)
	return
}

func (w *writer) shouldSendEndOfRecord() bool {
	return w.options.Get(EndOfRecord).EnabledForUs()
}

func (w *writer) shouldSendGoAhead() bool {
	return !w.options.Get(SuppressGoAhead).EnabledForUs()
}

const eventEndOfRecord event.Name = "internal.end-of-record"
const eventGoAhead event.Name = "internal.go-ahead"
const eventSend event.Name = "internal.send-data"

var ASCII encoding.Encoding = &asciiEncoding{}

type asciiEncoding struct{}

func (a asciiEncoding) NewDecoder() *encoding.Decoder {
	return &encoding.Decoder{Transformer: a}
}

func (a asciiEncoding) NewEncoder() *encoding.Encoder {
	return &encoding.Encoder{Transformer: a}
}

func (asciiEncoding) String() string { return "ASCII" }

func (a asciiEncoding) Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error) {
	for i, c := range src {
		if nDst >= len(dst) {
			err = transform.ErrShortDst
			break
		}
		if c < 128 {
			dst[nDst] = c
		} else {
			dst[nDst] = '\x1A'
		}
		nDst++
		nSrc = i + 1
	}
	return
}

func (a asciiEncoding) Reset() {}
