package main

import (
	"context"
	"io"

	"github.com/stesla/iris/internal/event"
	"github.com/stesla/iris/internal/telnet"
	"golang.org/x/text/encoding/unicode"
)

type session struct {
	telnet.Conn
	charset        telnet.CharsetHandler
	transmitBinary telnet.TransmitBinaryHandler
}

func newSession(conn telnet.Conn) *session {
	result := &session{
		Conn:    conn,
		charset: telnet.CharsetHandler{IsServer: true},
	}
	conn.RegisterHandler(&result.transmitBinary)
	conn.RegisterHandler(&result.charset)
	conn.ListenFunc(telnet.EventOption, func(ctx context.Context, ev event.Event) error {
		switch opt := ev.Data.(type) {
		case telnet.OptionData:
			switch opt.Option() {
			case telnet.Charset:
				if opt.ChangedUs && opt.EnabledForUs() {
					result.charset.RequestEncoding(unicode.UTF8)
				}
			}
		}
		return nil
	})
	return result
}

func (s *session) runForever() {
	s.GetOption(telnet.SuppressGoAhead).Allow(true, true).EnableBoth(s.Context())
	s.GetOption(telnet.EndOfRecord).Allow(true, true).EnableBoth(s.Context())
	s.GetOption(telnet.TransmitBinary).Allow(true, true).EnableBoth(s.Context())
	s.GetOption(telnet.Charset).Allow(true, true).EnableBoth(s.Context())
	io.Copy(s, s)
}
