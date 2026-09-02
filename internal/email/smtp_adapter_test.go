package email

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestSMTPAdapterSendHonorsCanceledDialContext(t *testing.T) {
	listener := newSMTPTestListener(t)
	adapter := newSMTPTestAdapter(t, listener.Addr())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := adapter.Send(ctx, smtpTestEmail())
	require.ErrorIs(t, err, context.Canceled)
}

func TestSMTPAdapterCloseRetiresPoolAndClosesReleasedConnections(t *testing.T) {
	adapter := &SMTPAdapter{
		id:   "smtp-close-test",
		pool: make(chan *smtpConn, smtpPoolSize),
	}
	require.NoError(t, adapter.Close())
	require.NoError(t, adapter.Close())
	require.True(t, adapter.closed.Load())

	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	adapter.releaseConn(&smtpConn{conn: local})
	_, err := remote.Read(make([]byte, 1))
	require.Error(t, err)

	_, err = adapter.acquireConn(t.Context())
	require.ErrorContains(t, err, "SMTP adapter is closed")
	kind, retryable, ok := DeliveryErrorDecision(err)
	require.True(t, ok)
	require.Equal(t, DeliveryErrorConnection, kind)
	require.True(t, retryable)
}

func TestSMTPAdapterSendCancelsBlockedProtocolIO(t *testing.T) {
	listener := newSMTPTestListener(t)
	adapter := newSMTPTestAdapter(t, listener.Addr())
	accepted := make(chan struct{})
	releaseServer := make(chan struct{})
	t.Cleanup(func() {
		close(releaseServer)
	})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		<-releaseServer
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	_, err := adapter.Send(ctx, smtpTestEmail())

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), time.Second)
	select {
	case <-accepted:
	default:
		t.Fatal("SMTP server did not accept the connection")
	}
}

func TestSMTPAdapterBuildMessageUsesSafeRFCHeadersAndTransferEncoding(t *testing.T) {
	adapter := &SMTPAdapter{from: (&mail.Address{Name: "Geul Mail", Address: "sender@example.test"}).String()}
	message, err := adapter.buildMessage(&Email{
		MessageID: "logical-command-1",
		To:        "recipient@example.test",
		Subject:   "안내 & 확인",
		Text:      "café\nsecond line",
	})
	require.NoError(t, err)
	require.NotContains(t, string(message), "Subject: 안내")
	require.Contains(t, string(message), "Content-Transfer-Encoding: quoted-printable\r\n")
	require.Contains(t, string(message), "caf=C3=A9\r\nsecond line")
	require.NotContains(t, strings.ReplaceAll(string(message), "\r\n", ""), "\n")

	parsed, err := mail.ReadMessage(bytes.NewReader(message))
	require.NoError(t, err)
	decodedSubject, err := (&mime.WordDecoder{}).DecodeHeader(parsed.Header.Get("Subject"))
	require.NoError(t, err)
	require.Equal(t, "안내 & 확인", decodedSubject)
	require.Regexp(t, `^<[0-9a-f]{64}@example\.test>$`, parsed.Header.Get("Message-ID"))
	body, err := io.ReadAll(parsed.Body)
	require.NoError(t, err)
	require.NotEmpty(t, body)
}

func TestSMTPAdapterBuildMessageRejectsHeaderInjection(t *testing.T) {
	adapter := &SMTPAdapter{from: "sender@example.test"}
	_, err := adapter.buildMessage(&Email{
		MessageID: "logical-command-1",
		To:        "recipient@example.test",
		Subject:   "safe\r\nBcc: attacker@example.test",
		Text:      "body",
	})
	require.ErrorContains(t, err, "header value contains a newline")
	kind, retryable, ok := DeliveryErrorDecision(err)
	require.True(t, ok)
	require.Equal(t, DeliveryErrorTemplate, kind)
	require.False(t, retryable)
}

func TestClassifySMTPDeliveryErrorRequiresAuthoritativeRecipientStatus(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantKind  DeliveryErrorKind
		retryable bool
	}{
		{
			name:      "mailbox does not exist",
			err:       fmt.Errorf("RCPT TO failed: %w", &textproto.Error{Code: 550, Msg: "5.1.1 mailbox does not exist"}),
			wantKind:  DeliveryErrorInvalidRecipient,
			retryable: false,
		},
		{
			name:      "bare 550 policy rejection",
			err:       fmt.Errorf("RCPT TO failed: %w", &textproto.Error{Code: 550, Msg: "mail rejected by policy"}),
			wantKind:  DeliveryErrorUnknown,
			retryable: false,
		},
		{
			name:      "sender address failure is not recipient evidence",
			err:       fmt.Errorf("MAIL FROM failed: %w", &textproto.Error{Code: 550, Msg: "5.1.1 sender rejected"}),
			wantKind:  DeliveryErrorUnknown,
			retryable: false,
		},
		{
			name:      "mailbox temporarily unavailable",
			err:       fmt.Errorf("RCPT TO failed: %w", &textproto.Error{Code: 450, Msg: "4.2.0 mailbox busy"}),
			wantKind:  DeliveryErrorUnknown,
			retryable: true,
		},
		{
			name:      "authentication credentials rejected",
			err:       fmt.Errorf("authentication failed: %w", &textproto.Error{Code: 535, Msg: "5.7.8 credentials invalid"}),
			wantKind:  DeliveryErrorAuthentication,
			retryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, retryable, ok := DeliveryErrorDecision(classifySMTPDeliveryError(tt.err))
			require.True(t, ok)
			require.Equal(t, tt.wantKind, kind)
			require.Equal(t, tt.retryable, retryable)
		})
	}
}

func TestClassifySMTPDeliveryErrorKeepsContextTerminationRetryable(t *testing.T) {
	for _, providerErr := range []error{context.Canceled, context.DeadlineExceeded} {
		classified := classifySMTPDeliveryError(providerErr)
		kind, retryable, ok := DeliveryErrorDecision(classified)
		require.True(t, ok)
		require.Equal(t, DeliveryErrorConnection, kind)
		require.True(t, retryable)
		require.ErrorIs(t, classified, providerErr)
	}
}

func newSMTPTestListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, listener.Close())
	})
	return listener
}

func newSMTPTestAdapter(t *testing.T, address net.Addr) *SMTPAdapter {
	t.Helper()
	host, rawPort, err := net.SplitHostPort(address.String())
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	adapter, err := NewSMTPAdapter(
		"smtp-test",
		"SMTP test",
		&model.SMTPAdapterConfig{
			Host:      host,
			Port:      port,
			FromEmail: "sender@example.test",
		},
	)
	require.NoError(t, err)
	return adapter
}

func smtpTestEmail() *Email {
	return &Email{
		MessageID: "<smtp-test@example.com>",
		To:        "recipient@example.test",
		Subject:   "SMTP cancellation test",
		Text:      "test",
	}
}
