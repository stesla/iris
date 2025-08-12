package telnet

import (
	"context"

	"github.com/stesla/iris/internal/event"
	"golang.org/x/text/encoding"
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
	d.ListenFunc(EventOption, h.handleOption)
}

func (h *TransmitBinaryHandler) handleOption(ctx context.Context, data any) error {
	encodable := ctx.Value(KeyEncodable).(Encodable)
	opt := data.(OptionData)
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
	return nil
}
