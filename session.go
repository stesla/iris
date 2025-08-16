package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/stesla/iris/internal/event"
	"github.com/stesla/iris/internal/telnet"
	"golang.org/x/text/encoding/unicode"
)

type session struct {
	telnet.Conn
	*bufio.Scanner
	logger         zerolog.Logger
	charset        telnet.CharsetHandler
	transmitBinary telnet.TransmitBinaryHandler
}

func newSession(conn telnet.Conn, logger zerolog.Logger) *session {
	result := &session{
		Conn:    conn,
		Scanner: bufio.NewScanner(conn),
		logger: logger.With().
			Str("client", conn.RemoteAddr().String()).
			Logger(),
		charset: telnet.CharsetHandler{IsServer: true},
	}
	conn.RegisterHandler(LogHandler{Logger: result.logger})
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
	s.logger.Debug().Msg("connected")
	s.GetOption(telnet.SuppressGoAhead).Allow(true, true).EnableBoth(s.Context())
	s.GetOption(telnet.EndOfRecord).Allow(true, true).EnableBoth(s.Context())
	s.GetOption(telnet.TransmitBinary).Allow(true, true).EnableBoth(s.Context())
	s.GetOption(telnet.Charset).Allow(true, true).EnableBoth(s.Context())
	for s.Scan() {
		switch command, rest, _ := strings.Cut(s.Text(), " "); command {
		case "connect":
			addr := strings.TrimSpace(rest)
			fmt.Fprintf(s, "connecting to %v...", addr)
			var p proxy
			if err := p.Initialize(s.logger, addr); err != nil {
				fmt.Fprintf(s, "error connecting (%v): %v", addr, err)
			}
			p.AddDownstream(s)
			io.Copy(&p, s)
		default:
			fmt.Fprintln(s, "unrecognized command:", s.Text())
		}
	}
	s.logger.Debug().Msg("disconnected")
}

type proxy struct {
	telnet.Conn
	logger zerolog.Logger
	mux    sync.Mutex

	downstream []io.WriteCloser

	charset        telnet.CharsetHandler
	transmitBinary telnet.TransmitBinaryHandler
}

func (p *proxy) Initialize(logger zerolog.Logger, addr string) error {
	tcp, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	p.Conn = telnet.Wrap(context.Background(), tcp)
	p.logger = logger.With().
		Str("server", p.Conn.RemoteAddr().String()).
		Logger()
	p.Conn.RegisterHandler(LogHandler{Logger: p.logger})
	p.Conn.RegisterHandler(&p.transmitBinary)
	p.Conn.RegisterHandler(&p.charset)
	p.Conn.ListenFunc(telnet.EventOption, func(ctx context.Context, ev event.Event) error {
		switch opt := ev.Data.(type) {
		case telnet.OptionData:
			switch opt.Option() {
			case telnet.Charset:
				if opt.ChangedUs && opt.EnabledForUs() {
					p.charset.RequestEncoding(unicode.UTF8)
				}
			}
		}
		return nil
	})
	go p.runForever()
	return nil
}

func (p *proxy) AddDownstream(w io.WriteCloser) {
	p.mux.Lock()
	defer p.mux.Unlock()
	p.downstream = append(p.downstream, w)
}

func (p *proxy) Close() error {
	p.Conn.Close()
	for _, w := range p.downstream {
		w.Close()
	}
	return nil
}

const proxyBufSize = 4096

func (p *proxy) runForever() {
	defer p.Close()
	p.logger.Debug().Msg("connected")
	ctx := p.Context()
	p.GetOption(telnet.SuppressGoAhead).Allow(true, true).EnableBoth(ctx)
	p.GetOption(telnet.EndOfRecord).Allow(true, true).EnableBoth(ctx)
	p.GetOption(telnet.TransmitBinary).Allow(true, true).EnableBoth(ctx)
	p.GetOption(telnet.Charset).Allow(true, true).EnableBoth(ctx)
	for {
		var buf = make([]byte, proxyBufSize)
		n, err := p.Read(buf)
		if err != nil {
			break
		}
		buf = buf[:n]
		p.sendDownstream(buf)
	}
	p.logger.Debug().Msg("disconnected")
}

func (p *proxy) sendDownstream(buf []byte) {
	p.mux.Lock()
	defer p.mux.Unlock()
	for _, w := range p.downstream {
		w.Write(buf)
	}
}
