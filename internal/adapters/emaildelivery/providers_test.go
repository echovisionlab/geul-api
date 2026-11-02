package emaildeliveryadapter_test

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
)

// A provider the administrator use cases open for one verification or test
// send keeps an SMTP connection pooled until they close it.
func TestMailProvidersCloseThePooledConnection(t *testing.T) {
	server := newFakeSMTPServer(t)
	provider, err := emaildeliveryadapter.MailProviders{}.Open(emaildelivery.MailAdapter{
		ID: "verify-config", Name: "Verification", Type: emaildelivery.AdapterTypeSMTP,
		Config: []byte(`{"host":"127.0.0.1","port":` + strconv.Itoa(int(server.port())) + `,"secure":false,"user":"smtp-user","password":"smtp-secret","from_email":"sender@example.test"}`),
	})
	require.NoError(t, err)

	_, err = provider.Send(t.Context(), &email.Email{To: "verified@example.test", Subject: "Verification", HTML: "<p>ok</p>", Text: "ok"})
	require.NoError(t, err)
	require.Equal(t, []string{"verified@example.test"}, server.recipients())
	require.NoError(t, provider.Close())
	server.requireClientsDisconnected(t)
}

func TestMailProvidersRejectAConfigTheFactoryRefuses(t *testing.T) {
	_, err := emaildeliveryadapter.MailProviders{}.Open(emaildelivery.MailAdapter{Type: emaildelivery.AdapterTypeLogging, Config: []byte(`{}`)})
	require.EqualError(t, err, "logging mail adapter is non-delivery and cannot be activated")
	_, err = emaildeliveryadapter.MailProviders{}.Open(emaildelivery.MailAdapter{Type: emaildelivery.AdapterTypeSES, Config: []byte(`{bad`)})
	require.ErrorContains(t, err, "invalid SES config")
}

// fakeSMTPServer speaks just enough plaintext SMTP for provider verification
// and records each accepted recipient.
type fakeSMTPServer struct {
	listener net.Listener
	mu       sync.Mutex
	rcpts    []string
	conns    []net.Conn
	open     int
	idle     chan struct{}
	wasIdle  bool
	wg       sync.WaitGroup
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &fakeSMTPServer{listener: listener, idle: make(chan struct{})}
	server.wg.Add(1)
	go server.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		server.mu.Lock()
		for _, conn := range server.conns {
			_ = conn.Close()
		}
		server.mu.Unlock()
		server.wg.Wait()
	})
	return server
}

func (s *fakeSMTPServer) port() int32 {
	return int32(s.listener.Addr().(*net.TCPAddr).Port)
}

func (s *fakeSMTPServer) recipients() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.rcpts...)
}

// requireClientsDisconnected waits until every client connection ended. A
// provider that is never closed keeps its pooled connection open.
func (s *fakeSMTPServer) requireClientsDisconnected(t *testing.T) {
	t.Helper()
	select {
	case <-s.idle:
	case <-time.After(10 * time.Second):
		t.Fatal("a provider connection is still open")
	}
}

func (s *fakeSMTPServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.open++
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
			s.mu.Lock()
			s.open--
			if s.open == 0 && !s.wasIdle {
				s.wasIdle = true
				close(s.idle)
			}
			s.mu.Unlock()
		}()
	}
}

func (s *fakeSMTPServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	reply := func(lines ...string) bool {
		for _, line := range lines {
			if _, err := conn.Write([]byte(line + "\r\n")); err != nil {
				return false
			}
		}
		return true
	}
	if !reply("220 fake.test ESMTP") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		var ok bool
		switch {
		case strings.HasPrefix(command, "EHLO"):
			ok = reply("250-fake.test", "250 AUTH PLAIN")
		case strings.HasPrefix(command, "AUTH"):
			ok = reply("235 2.7.0 accepted")
		case strings.HasPrefix(command, "MAIL FROM"):
			ok = reply("250 2.1.0 ok")
		case strings.HasPrefix(command, "RCPT TO"):
			address := strings.Trim(strings.TrimSpace(line)[len("RCPT TO:"):], "<> ")
			s.mu.Lock()
			s.rcpts = append(s.rcpts, address)
			s.mu.Unlock()
			ok = reply("250 2.1.5 ok")
		case command == "DATA":
			if !reply("354 end with .") {
				return
			}
			for {
				body, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if body == ".\r\n" {
					break
				}
			}
			ok = reply("250 2.0.0 queued")
		case command == "QUIT":
			_ = reply("221 bye")
			return
		default:
			ok = reply("250 ok")
		}
		if !ok {
			return
		}
	}
}

func TestMailProvidersRetainConfigDecodeDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		kind   emaildelivery.AdapterType
		config string
		want   string
	}{
		{"SES root", emaildelivery.AdapterTypeSES, `[]`, "invalid SES config: json: cannot unmarshal array into Go value of type model.SESAdapterConfig"},
		{"SMTP root", emaildelivery.AdapterTypeSMTP, `true`, "invalid SMTP config: json: cannot unmarshal bool into Go value of type model.SMTPAdapterConfig"},
		{"SES field", emaildelivery.AdapterTypeSES, `{"region":123}`, "invalid SES config: json: cannot unmarshal number into Go struct field SESAdapterConfig.region of type string"},
		{"SMTP field", emaildelivery.AdapterTypeSMTP, `{"port":"invalid"}`, "invalid SMTP config: json: cannot unmarshal string into Go struct field SMTPAdapterConfig.port of type int"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := emaildeliveryadapter.MailProviders{}.Open(emaildelivery.MailAdapter{Type: tc.kind, Config: []byte(tc.config)})
			require.EqualError(t, err, tc.want)
		})
	}
}
