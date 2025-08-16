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
	conn           telnet.Conn
	logger         zerolog.Logger
	charset        telnet.CharsetHandler
	transmitBinary telnet.TransmitBinaryHandler
}

func newSession(conn telnet.Conn, logger zerolog.Logger) *session {
	s := &session{
		conn:   conn,
		logger: logger,
	}
	s.conn.RegisterHandler(LogHandler{Logger: s.logger})
	s.conn.RegisterHandler(&s.transmitBinary)
	s.conn.RegisterHandler(&s.charset)
	s.conn.ListenFunc(telnet.EventOption, s.handleEvent)
	return s
}

func (s *session) Close() error {
	return s.conn.Close()
}

func (s *session) Context() context.Context {
	return s.conn.Context()
}

func (s *session) GetOption(opt byte) telnet.OptionState {
	return s.conn.GetOption(opt)
}

func (s *session) Read(p []byte) (n int, err error) {
	return s.conn.Read(p)
}

func (s *session) Write(p []byte) (n int, err error) {
	return s.conn.Write(p)
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

func (s *downstreamSession) authenticate() bool {
	if s.Scan() {
		return s.Text() == "login "+*password
	}
	return false
}

func (s *downstreamSession) findUpstream() (*upstreamSession, error) {
	for s.Scan() {
		switch command, rest, _ := strings.Cut(s.Text(), " "); command {
		case "connect":
			addr := strings.TrimSpace(rest)
			fmt.Fprintf(s, "connecting to %v...", addr)
			upstream := &upstreamSession{}
			if err := upstream.Initialize(addr); err != nil {
				fmt.Fprintf(s, "error connecting (%v): %v", addr, err)
			}
			upstream.AddDownstream(s)
			return upstream, nil
		default:
			fmt.Fprintln(s, "unrecognized command:", s.Text())
		}
	}
	// the only case where we ever get here is if we fail to scan, which will
	// only happen if the client disconnected
	return nil, io.EOF
}

func (s *downstreamSession) runForever() {
	s.logger.Debug().Msg("connected")
	defer s.logger.Debug().Msg("disconnected")

	s.negotiateOptions()
	if !s.authenticate() {
		return
	}
	upstream, err := s.findUpstream()
	if err != nil {
		fmt.Fprintln(s, "error connecting upstream:", err)
		return
	}
	io.Copy(upstream, s)
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
	s.conn.Close()
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
