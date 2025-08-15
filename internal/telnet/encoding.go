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
	SetReadEncoding(encoding.Encoding)
	SetWriteEncoding(encoding.Encoding)
}

func SetEncoding(ctx context.Context, enc encoding.Encoding) {
	encodable := ctx.Value(KeyEncodable).(Encodable)
	encodable.SetReadEncoding(enc)
	encodable.SetWriteEncoding(enc)
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

type TransmitBinaryHandler struct {
	ctx context.Context
}

func (h *TransmitBinaryHandler) Register(ctx context.Context) {
	h.ctx = ctx

	GetOption(ctx, TransmitBinary).Allow(true, true)

	d := ctx.Value(KeyDispatcher).(event.Dispatcher)
	d.Listen(EventOption, h)
}

func (h *TransmitBinaryHandler) Unregister() {
	d := h.ctx.Value(KeyDispatcher).(event.Dispatcher)
	d.RemoveListener(EventOption, h)

	opt := GetOption(h.ctx, TransmitBinary)
	opt.Allow(false, false)
	opt.DisableForThem(h.ctx)
	opt.DisableForUs(h.ctx)

	SetEncoding(h.ctx, ASCII)
}

func (h *TransmitBinaryHandler) Listen(ctx context.Context, ev event.Event) error {
	switch opt := ev.Data.(type) {
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
	ctx context.Context
	enc encoding.Encoding
}

func (h *CharsetHandler) Register(ctx context.Context) {
	h.ctx = ctx

	GetOption(ctx, Charset).Allow(true, true)

	d := ctx.Value(KeyDispatcher).(event.Dispatcher)
	d.Listen(EventOption, h)
	d.Listen(EventSubnegotiation, h)
	d.Listen(EventCharsetAccepted, h)
	d.Listen(EventCharsetRejected, h)
}

func (h *CharsetHandler) Unregister() {
	GetOption(h.ctx, Charset).Allow(false, false)

	d := h.ctx.Value(KeyDispatcher).(event.Dispatcher)
	d.RemoveListener(EventCharsetRejected, h)
	d.RemoveListener(EventCharsetAccepted, h)
	d.RemoveListener(EventSubnegotiation, h)
	d.RemoveListener(EventOption, h)
}

func (h *CharsetHandler) Listen(ctx context.Context, ev event.Event) error {
	switch t := ev.Data.(type) {
	case CharsetData:
		h.enc = t.Encoding
		opt := GetOption(ctx, TransmitBinary)
		if them, us := opt.EnabledForThem(), opt.EnabledForUs(); them && us {
			SetEncoding(ctx, h.enc)
		}
	case OptionData:
		switch t.Option() {
		case TransmitBinary:
			if them, us := t.EnabledForThem(), t.EnabledForUs(); them && us {
				SetEncoding(ctx, h.enc)
			} else {
				SetEncoding(ctx, ASCII)
			}
		}
	case Subnegotiation:
		switch t.Opt {
		case Charset:
			if GetOption(ctx, Charset).EnabledForUs() {
				switch cmd, data := t.Data[0], t.Data[1:]; cmd {
				case CharsetAccepted:
					enc := h.getEncoding(data)
					Dispatch(ctx, event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: enc}})
				case CharsetRejected:
					Dispatch(ctx, event.Event{Name: EventCharsetRejected})
				case CharsetRequest:
					return h.handleCharsetRequest(ctx, data)
				case CharsetTTableIs:
					Dispatch(ctx, event.Event{Name: EventSend, Data: []byte{IAC, SB, Charset, CharsetTTableRejected, IAC, SE}})
				}
			}
		}
	}
	return nil
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
		Dispatch(ctx, event.Event{Name: EventSend, Data: []byte{IAC, SB, Charset, CharsetRejected, IAC, SE}})
	} else {
		out := []byte{IAC, SB, Charset, CharsetAccepted}
		out = append(out, charset...)
		out = append(out, IAC, SE)
		Dispatch(ctx, event.Event{Name: EventSend, Data: out})
		Dispatch(ctx, event.Event{Name: EventCharsetAccepted, Data: CharsetData{Encoding: enc}})
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
