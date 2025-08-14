package telnet

import (
	"bytes"
	"context"

	"github.com/stesla/iris/internal/event"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/transform"
)

type Encodable interface {
	SetEncoding(encoding.Encoding)
	SetReadEncoding(encoding.Encoding)
	SetWriteEncoding(encoding.Encoding)
}

func (c *conn) SetEncoding(enc encoding.Encoding) {
	c.SetReadEncoding(enc)
	c.SetWriteEncoding(enc)
}

func (c *conn) SetReadEncoding(enc encoding.Encoding) {
	c.read = enc.NewDecoder().Reader(c.readNoEnc)
}

func (c *conn) SetWriteEncoding(enc encoding.Encoding) {
	c.write = enc.NewEncoder().Writer(c.writeNoEnc)
}

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

type TransmitBinaryHandler struct{}

func (h *TransmitBinaryHandler) Register(ctx context.Context) {
	options := ctx.Value(KeyOptionMap).(OptionMap)
	options.Get(TransmitBinary).Allow(true, true)

	d := ctx.Value(KeyDispatcher).(event.Dispatcher)
	d.Listen(EventOption, h)
}

func (h *TransmitBinaryHandler) Unregister(ctx context.Context) {
	d := ctx.Value(KeyDispatcher).(event.Dispatcher)
	d.RemoveListener(EventOption, h)

	options := ctx.Value(KeyOptionMap).(OptionMap)
	options.Get(TransmitBinary).Allow(false, false)
	options.Get(TransmitBinary).DisableForThem(ctx)
	options.Get(TransmitBinary).DisableForUs(ctx)

	encodable := ctx.Value(KeyEncodable).(Encodable)
	encodable.SetEncoding(ASCII)
}

func (h *TransmitBinaryHandler) Listen(ctx context.Context, data any) error {
	switch opt := data.(type) {
	case OptionData:
		switch opt.OptionState.Option() {
		case TransmitBinary:
			encodable := ctx.Value(KeyEncodable).(Encodable)
			if opt.ChangedUs {
				if opt.EnabledForUs() {
					encodable.SetWriteEncoding(encoding.Nop)
				} else {
					encodable.SetWriteEncoding(ASCII)
				}

			}
			if opt.ChangedThem {
				if opt.EnabledForThem() {
					encodable.SetReadEncoding(encoding.Nop)
				} else {
					encodable.SetReadEncoding(ASCII)
				}
			}
		}
	}
	return nil
}

type CharsetHandler struct {
}

func (h *CharsetHandler) Register(ctx context.Context) {
	options := ctx.Value(KeyOptionMap).(OptionMap)
	options.Get(Charset).Allow(true, true)

	d := ctx.Value(KeyDispatcher).(event.Dispatcher)
	d.Listen(EventOption, h)
	d.Listen(eventSubnegotiation, h)
}

func (h *CharsetHandler) Unregister(ctx context.Context) {
	d := ctx.Value(KeyDispatcher).(event.Dispatcher)
	d.RemoveListener(eventSubnegotiation, h)
	d.RemoveListener(EventOption, h)

	options := ctx.Value(KeyOptionMap).(OptionMap)
	options.Get(Charset).Allow(false, false)
	options.Get(Charset).DisableForThem(ctx)
	options.Get(Charset).DisableForUs(ctx)

	encodable := ctx.Value(KeyEncodable).(Encodable)
	// TODO: this should be more intelligent
	encodable.SetEncoding(encoding.Nop)
}

func (h *CharsetHandler) Listen(ctx context.Context, data any) error {
	switch opt := data.(type) {
	case OptionData:
		switch opt.OptionState.Option() {
		case Charset:
		case TransmitBinary:
		}
	case *subnegotiation:
		switch opt.opt {
		case Charset:
			switch cmd, data := opt.data[0], opt.data[1:]; cmd {
			case CharsetAccepted:
				enc := h.getEncoding(data)
				Dispatch(ctx, EventCharsetAccepted, CharsetData{Encoding: enc})
			case CharsetRejected:
				Dispatch(ctx, EventCharsetRejected, nil)
			case CharsetRequest:
				return h.handleCharsetRequest(ctx, data)
			case CharsetTTableIs:
				Dispatch(ctx, eventSend, []byte{IAC, SB, Charset, CharsetTTableRejected, IAC, SE})
			}
		}
	}
	return nil
}

func Dispatch(ctx context.Context, eventName event.Name, data any) {
	d := ctx.Value(KeyDispatcher).(event.Dispatcher)
	d.Dispatch(ctx, eventName, data)
}

func (h *CharsetHandler) handleCharsetRequest(ctx context.Context, data []byte) error {
	var charset []byte
	var enc encoding.Encoding

	const ttable = "[TTABLE]"
	if len(data) > 10 && bytes.HasPrefix(data, []byte(ttable)) {
		// We don't support TTABLE, so we're just going to strip off the
		// version byte, but according to RFC 2066 it should basically always
		// be 0x01. If we ever add TTABLE support, we'll want to check the
		// version to see if it's a version we support.
		data = data[len(ttable)+1:]
	}

	if len(data) > 2 {
		charset, enc = h.selectEncoding(bytes.Split(data[1:], data[0:1]))
	}

	if enc == nil {
		Dispatch(ctx, eventSend, []byte{IAC, SB, Charset, CharsetRejected, IAC, SE})
	} else {
		out := []byte{IAC, SB, Charset, CharsetAccepted}
		out = append(out, charset...)
		out = append(out, IAC, SE)
		Dispatch(ctx, eventSend, out)
		Dispatch(ctx, EventCharsetAccepted, CharsetData{Encoding: enc})
	}
	return nil
}

func (h *CharsetHandler) selectEncoding(names [][]byte) ([]byte, encoding.Encoding) {
	for _, name := range names {
		enc := h.getEncoding(name)
		if enc != nil {
			return name, enc
		}
	}
	return nil, nil
}

func (*CharsetHandler) getEncoding(name []byte) encoding.Encoding {
	switch s := string(name); s {
	case "US-ASCII":
		return ASCII
	default:
		enc, _ := ianaindex.IANA.Encoding(s)
		return enc
	}
}
