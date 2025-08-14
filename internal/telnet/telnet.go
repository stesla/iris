package telnet

import (
	"context"
	"io"
	"net"

	"github.com/stesla/iris/internal/event"
)

type Conn interface {
	net.Conn
	event.Dispatcher
	Encodable
	OptionMap

	Context() context.Context
	RegisterHandler(Handler) (unregister func())
}

type conn struct {
	net.Conn
	event.Dispatcher
	OptionMap

	ctx context.Context

	readNoEnc, read   io.Reader
	writeNoEnc, write io.Writer
}

func Dial(address string) (Conn, error) {
	tcpconn, err := net.Dial("tcp", address)
	return Wrap(tcpconn), err
}

func Wrap(c net.Conn) Conn {
	return wrap(c)
}

type contextKey int

const (
	KeyDispatcher contextKey = 0 + iota
	KeyOptionMap
	KeyEncodable
)

func wrap(c net.Conn) *conn {
	dispatcher := event.NewDispatcher()
	options := NewOptionMap()
	cc := &conn{
		Conn:       c,
		Dispatcher: dispatcher,
		OptionMap:  options,
		ctx:        context.Background(),
	}
	cc.ctx = context.WithValue(cc.ctx, KeyDispatcher, cc)
	cc.ctx = context.WithValue(cc.ctx, KeyOptionMap, cc)
	cc.ctx = context.WithValue(cc.ctx, KeyEncodable, cc)
	cc.readNoEnc = &reader{in: c, ctx: cc.ctx}
	cc.writeNoEnc = &writer{out: c, ctx: cc.ctx}
	cc.SetEncoding(ASCII)
	cc.ListenFunc(eventNegotation, cc.handleNegotiation)
	cc.ListenFunc(eventSend, cc.handleSend)
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

func (c *conn) handleSend(_ context.Context, data any) error {
	_, err := c.Conn.Write(data.([]byte))
	return err
}

func (c *conn) Context() context.Context { return c.ctx }

func (c *conn) Read(p []byte) (n int, err error) {
	return c.read.Read(p)
}

type Handler interface {
	Register(ctx context.Context)
	Unregister(ctx context.Context)
}

func (c *conn) RegisterHandler(h Handler) func() {
	h.Register(c.ctx)
	return func() {
		h.Unregister(c.ctx)
	}
}

func (c *conn) Write(p []byte) (n int, err error) {
	return c.write.Write(p)
}

type reader struct {
	in  io.Reader
	ctx context.Context

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

	d := r.ctx.Value(KeyDispatcher).(event.Dispatcher)

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
				d.Dispatch(r.ctx, eventEndOfRecord, nil)
				r.ds = decodeByte
			case GA:
				d.Dispatch(r.ctx, eventGoAhead, nil)
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
			d.Dispatch(r.ctx, eventNegotation, &negotiation{r.cmd, buf[0]})
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
				d.Dispatch(r.ctx, eventSubnegotiation, &subnegotiation{
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
	out io.Writer
	ctx context.Context
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

func (w *writer) options() OptionMap {
	return w.ctx.Value(KeyOptionMap).(OptionMap)
}

func (w *writer) shouldSendEndOfRecord() bool {
	return w.options().Get(EndOfRecord).EnabledForUs()
}

func (w *writer) shouldSendGoAhead() bool {
	return !w.options().Get(SuppressGoAhead).EnabledForUs()
}
