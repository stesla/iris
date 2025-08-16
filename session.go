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

func (s *session) negotiateOptions() {
	opts := []byte{
		telnet.SuppressGoAhead,
		telnet.EndOfRecord,
		telnet.TransmitBinary,
		telnet.Charset,
	}
	for _, opt := range opts {
		s.GetOption(opt).Allow(true, true).EnableBoth(s.Context())
	}
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
	s.negotiateOptions()
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

func (s *upstreamSession) Initialize(addr string) error {
	tcp, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	conn := telnet.Wrap(context.Background(), tcp)
	s.session = newSession(conn, logger.With().
		Str("server", conn.RemoteAddr().String()).
		Logger())
	go s.runForever()
	return nil
}

func (s *upstreamSession) AddDownstream(w io.WriteCloser) {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.downstream = append(s.downstream, w)
}

func (s *upstreamSession) Close() error {
	s.Conn.Close()
	for _, w := range s.downstream {
		w.Close()
	}
	return nil
}

const proxyBufSize = 4096

func (s *upstreamSession) runForever() {
	defer s.Close()
	s.logger.Debug().Msg("connected")
	s.negotiateOptions()
	for {
		var buf = make([]byte, proxyBufSize)
		n, err := s.Read(buf)
		if err != nil {
			break
		}
		buf = buf[:n]
		s.sendDownstream(buf)
	}
	s.logger.Debug().Msg("disconnected")
}

func (s *upstreamSession) sendDownstream(buf []byte) {
	s.mux.Lock()
	defer s.mux.Unlock()
	for _, w := range s.downstream {
		w.Write(buf)
	}
}
