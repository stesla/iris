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
	logger         zerolog.Logger
	charset        telnet.CharsetHandler
	transmitBinary telnet.TransmitBinaryHandler
}

func newSession(conn telnet.Conn, baselog zerolog.Logger) *session {
	s := &session{
		Conn:   conn,
		logger: baselog,
	}
	s.RegisterHandler(LogHandler{Logger: s.logger})
	s.RegisterHandler(&s.transmitBinary)
	s.RegisterHandler(&s.charset)
	s.ListenFunc(telnet.EventOption, s.handleEvent)
	return s
}

func (s *session) handleEvent(_ context.Context, ev event.Event) error {
	switch opt := ev.Data.(type) {
	case telnet.OptionData:
		switch opt.Option() {
		case telnet.Charset:
			if opt.ChangedUs && opt.EnabledForUs() {
				s.charset.RequestEncoding(unicode.UTF8)
			}
		}
	}
	return nil
}

type downstreamSession struct {
	*session
	*bufio.Scanner
}

func newDownstreamSession(conn telnet.Conn) *downstreamSession {
	result := &downstreamSession{
		session: newSession(conn, logger.With().
			Str("client", conn.RemoteAddr().String()).
			Logger()),
		Scanner: bufio.NewScanner(conn),
	}
	result.charset.IsServer = true
	return result
}

func (s *downstreamSession) runForever() {
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
			var upstream upstreamSession
			if err := upstream.Initialize(addr); err != nil {
				fmt.Fprintf(s, "error connecting (%v): %v", addr, err)
			}
			upstream.AddDownstream(s)
			io.Copy(&upstream, s)
		default:
			fmt.Fprintln(s, "unrecognized command:", s.Text())
		}
	}
	s.logger.Debug().Msg("disconnected")
}

type upstreamSession struct {
	*session
	mux        sync.Mutex
	downstream []io.WriteCloser
}

func (p *upstreamSession) Initialize(addr string) error {
	tcp, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	conn := telnet.Wrap(context.Background(), tcp)
	p.session = newSession(conn, logger.With().
		Str("server", conn.RemoteAddr().String()).
		Logger())
	go p.runForever()
	return nil
}

func (p *upstreamSession) AddDownstream(w io.WriteCloser) {
	p.mux.Lock()
	defer p.mux.Unlock()
	p.downstream = append(p.downstream, w)
}

func (p *upstreamSession) Close() error {
	p.Conn.Close()
	for _, w := range p.downstream {
		w.Close()
	}
	return nil
}

const proxyBufSize = 4096

func (p *upstreamSession) runForever() {
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

func (p *upstreamSession) sendDownstream(buf []byte) {
	p.mux.Lock()
	defer p.mux.Unlock()
	for _, w := range p.downstream {
		w.Write(buf)
	}
}
