package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/stesla/iris/internal/event"
	"github.com/stesla/iris/internal/telnet"
	"golang.org/x/text/encoding/unicode"
)

const readBufSize = 4096

var (
	sessionsMutex sync.Mutex
	sessions      = make(map[string]*upstreamSession)
)

func sessionForKey(key string) *upstreamSession {
	sessionsMutex.Lock()
	defer sessionsMutex.Unlock()
	if _, found := sessions[key]; !found {
		sessions[key] = &upstreamSession{key: key}
	}
	return sessions[key]
}

func deleteSessionWithKey(key string) {
	sessionsMutex.Lock()
	defer sessionsMutex.Unlock()
	delete(sessions, key)
}

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
	upstream *upstreamSession
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

func (s *downstreamSession) connectNewUpstream(rest string, buf bytes.Buffer) error {
	addr := strings.TrimSpace(rest)
	fmt.Fprintf(s, "connecting to %v...", addr)
	if err := s.upstream.Connect(addr); err != nil {
		return fmt.Errorf("error connecting (%v): %w", addr, err)
	}
	if _, err := s.upstream.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("error writing to (%v): %w", addr, err)
	}
	return nil
}

func (s *downstreamSession) findUpstream() error {
	var buf bytes.Buffer
	for s.Scan() {
		switch command, rest, _ := strings.Cut(s.Text(), " "); command {
		case "connect":
			if s.upstream == nil {
				return errors.New("you must select an upstream to connect")
			}
			return s.connectNewUpstream(rest, buf)
		case "send":
			fmt.Fprintln(&buf, rest)
		case "upstream":
			s.upstream = sessionForKey(rest)
			s.upstream.AddDownstream(s)
			if s.upstream.IsConnected() {
				_, err := s.upstream.history.WriteTo(s)
				return err
			}
		default:
			fmt.Fprintln(s, "unrecognized command:", s.Text())
		}
	}
	// the only case where we ever get here is if we fail to scan, which will
	// only happen if the client disconnected
	return io.EOF
}

func (s *downstreamSession) runForever() {
	s.logger.Debug().Msg("connected")
	defer s.logger.Debug().Msg("disconnected")

	s.negotiateOptions()
	if !s.authenticate() {
		return
	}
	err := s.findUpstream()
	if err != nil {
		fmt.Fprintln(s, "error connecting upstream:", err)
		return
	}
	io.Copy(s.upstream, s)
}

type upstreamSession struct {
	*session
	key        string
	mux        sync.Mutex
	downstream []io.WriteCloser
	history    History
}

func (s *upstreamSession) Connect(addr string) (err error) {
	s.history, err = newHistory(s.key)
	if err != nil {
		return
	}
	s.AddDownstream(s.history)

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

func (s *upstreamSession) IsConnected() bool {
	return s.session != nil
}

func (s *upstreamSession) runForever() {
	defer deleteSessionWithKey(s.key)
	defer s.Close()
	s.logger.Debug().Msg("connected")
	s.negotiateOptions()
	for {
		var buf = make([]byte, readBufSize)
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

type History interface {
	io.WriteCloser
	io.WriterTo
}

const defaultHistorySize = 20 * 1024 // about 256 lines of text
const logTimeFormat = "2006-01-02 15:04:05 -0700 MST"
const logSeperator = "--------------- %s - %s ---------------\n"
const logSepOpened = "--------------- opened"

func newHistory(key string) (History, error) {
	log := &logFile{key: key, historySize: defaultHistorySize}
	if err := log.Open(); err != nil {
		return nil, fmt.Errorf("error opening log for key (%v): %w", key, err)
	}
	return log, nil
}

type logFile struct {
	*os.File
	key         string
	historySize int64
}

func (f *logFile) Open() (err error) {
	logFileName := path.Join(
		*logdir,
		fmt.Sprintf("%s-%s.log", time.Now().Format("2006-01-02"), f.key),
	)
	f.File, err = os.OpenFile(
		logFileName,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0644,
	)
	if err == nil {
		t := time.Now()
		fmt.Fprintf(f, logSeperator, "opened", t.Format(logTimeFormat))
	}
	return
}

func (f *logFile) Close() (err error) {
	t := time.Now()
	fmt.Fprintf(f, logSeperator, "closed", t.Format(logTimeFormat))
	return f.File.Close()
}

func (f *logFile) WriteTo(w io.Writer) (int64, error) {
	if f.File == nil {
		return 0, errors.New("log file not open")
	}
	file, err := os.Open(f.Name())
	if err != nil {
		return 0, err
	}
	defer file.Close()
	end, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		return 0, err
	}
	if end > f.historySize {
		_, err = file.Seek(end-f.historySize, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
	}
	buf := make([]byte, f.historySize)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return 0, err
	}
	buf = buf[:n]
	if n = bytes.LastIndex(buf, []byte(logSepOpened)); n > 0 {
		buf = buf[n:]
		n = bytes.IndexByte(buf, '\n')
		buf = buf[n+1:]
	}
	n, err = w.Write(buf)
	return int64(n), err
}
