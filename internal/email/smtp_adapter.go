package email

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// SMTPAdapter sends emails using SMTP with connection pooling.
type SMTPAdapter struct {
	id       string
	name     string
	host     string
	port     int
	secure   bool
	user     string
	password string
	from     string
	pool     chan *smtpConn
	closed   atomic.Bool
}

// smtpConn wraps an SMTP client with its underlying connection.
type smtpConn struct {
	client  *smtp.Client
	conn    net.Conn
	created time.Time
}

const (
	smtpPoolSize       = 5
	smtpMaxConnAge     = 5 * time.Minute
	smtpConnectTimeout = 30 * time.Second
	smtpIOTimeout      = 30 * time.Second
)

// NewSMTPAdapter creates a new SMTPAdapter from configuration.
func NewSMTPAdapter(id, name string, cfg *model.SMTPAdapterConfig) (*SMTPAdapter, error) {
	// Validate required fields
	if cfg.Host == "" {
		return nil, fmt.Errorf("SMTP host is required")
	}
	if cfg.Port == 0 {
		return nil, fmt.Errorf("SMTP port is required")
	}
	if cfg.FromEmail == "" {
		return nil, fmt.Errorf("SMTP from email is required")
	}
	if !emailRegex.MatchString(cfg.FromEmail) {
		return nil, fmt.Errorf("invalid SMTP from email format: %s", cfg.FromEmail)
	}
	if err := rejectHeaderNewlines(cfg.FromName); err != nil {
		return nil, fmt.Errorf("invalid SMTP from name: %w", err)
	}

	// Build from address with RFC 5322/2047-safe display-name encoding.
	from := cfg.FromEmail
	if cfg.FromName != "" {
		from = (&mail.Address{Name: cfg.FromName, Address: cfg.FromEmail}).String()
	}

	adapter := &SMTPAdapter{
		id:       id,
		name:     name,
		host:     cfg.Host,
		port:     cfg.Port,
		secure:   cfg.Secure,
		user:     cfg.User,
		password: cfg.Password,
		from:     from,
		pool:     make(chan *smtpConn, smtpPoolSize),
	}

	// NOTE: Connection verification is done lazily on first send attempt.
	// This avoids startup failures when SMTP server is temporarily unavailable
	// (e.g., in containerized environments where services start in parallel).

	slog.Info("SMTP adapter created",
		"adapter_id", id,
		"adapter_name", name,
		"host", cfg.Host,
		"port", cfg.Port,
		"secure", cfg.Secure,
	)

	return adapter, nil
}

// ID returns the adapter ID.
func (a *SMTPAdapter) ID() string { return a.id }

// Name returns the adapter name.
func (a *SMTPAdapter) Name() string { return a.name }

// Type returns the adapter type.
func (a *SMTPAdapter) Type() model.MailAdapterType {
	return model.MailAdapterType(managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP.String())
}

// Close retires the adapter and closes every idle pooled connection. Active
// sends are allowed to finish, but their connections are closed instead of
// returning to a retired pool.
func (a *SMTPAdapter) Close() error {
	if a == nil || a.closed.Swap(true) {
		return nil
	}
	for {
		select {
		case sc := <-a.pool:
			a.closeConn(sc)
		default:
			return nil
		}
	}
}

// Send sends an email using SMTP with connection pooling.
func (a *SMTPAdapter) Send(ctx context.Context, email *Email) (result *SendResult, sendErr error) {
	defer func() {
		if sendErr != nil {
			logAdapterLifecycle(
				ctx,
				slog.LevelError,
				"Email provider request failed",
				"mail.provider.failed",
				a.id,
				a.name,
				email,
				"",
				"failed",
				"provider_request_failed",
				"provider_error",
			)
			sendErr = classifySMTPDeliveryError(sendErr)
		}
	}()
	msg, err := a.buildMessage(email)
	if err != nil {
		sendErr = err
		return nil, err
	}

	// Try to get a pooled connection
	sc, err := a.acquireConn(ctx)
	if err != nil {
		sendErr = err
		return nil, err
	}

	sc, err = a.sendWithRetry(ctx, sc, email, msg)
	if err != nil {
		sendErr = err
		return nil, err
	}

	a.recycleConn(ctx, sc)

	logAdapterLifecycle(
		ctx,
		slog.LevelInfo,
		"Email accepted by provider",
		"mail.provider.accepted",
		a.id,
		a.name,
		email,
		email.MessageID,
		"accepted",
		"",
		"",
	)

	return &SendResult{MessageID: email.MessageID}, nil
}

func (a *SMTPAdapter) sendWithRetry(
	ctx context.Context,
	sc *smtpConn,
	email *Email,
	msg []byte,
) (*smtpConn, error) {
	err := a.sendWithConn(ctx, sc, email, msg)
	if err == nil {
		return sc, nil
	}
	a.closeConn(sc)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	classified := classifySMTPDeliveryError(err)
	_, retryable, _ := DeliveryErrorDecision(classified)
	if !retryable {
		return nil, classified
	}

	freshConn, err := a.newConn(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	if err := a.sendWithConn(ctx, freshConn, email, msg); err != nil {
		a.closeConn(freshConn)
		return nil, err
	}
	return freshConn, nil
}

func (a *SMTPAdapter) recycleConn(ctx context.Context, sc *smtpConn) {
	if err := withSMTPConnContext(ctx, sc.conn, sc.client.Reset); err != nil {
		a.closeConn(sc)
		return
	}
	a.releaseConn(sc)
}

func classifySMTPDeliveryError(err error) error {
	if err == nil {
		return nil
	}
	var existing *DeliveryError
	if errors.As(err, &existing) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return NewDeliveryError(DeliveryErrorConnection, true, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewDeliveryError(DeliveryErrorConnection, true, err)
	}
	var protocolErr *textproto.Error
	if errors.As(err, &protocolErr) {
		switch {
		case protocolErr.Code == 535:
			return NewDeliveryError(DeliveryErrorAuthentication, false, err)
		case protocolErr.Code >= 400 && protocolErr.Code < 500:
			// SMTP defines every 4yz reply as transient, but the reply code alone
			// does not prove rate limiting or a network timeout.
			return NewDeliveryError(DeliveryErrorUnknown, true, err)
		case smtpErrorAuthoritativelyRejectsRecipient(err, protocolErr):
			return NewDeliveryError(DeliveryErrorInvalidRecipient, false, err)
		default:
			return NewDeliveryError(DeliveryErrorUnknown, false, err)
		}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return NewDeliveryError(DeliveryErrorConnection, true, err)
	}
	return NewDeliveryError(DeliveryErrorUnknown, false, err)
}

func smtpErrorAuthoritativelyRejectsRecipient(err error, protocolErr *textproto.Error) bool {
	if protocolErr == nil || !strings.Contains(err.Error(), "RCPT TO failed") {
		return false
	}
	// A bare SMTP 550-class status is not enough: it can mean policy, content,
	// authentication, or sender rejection. Only enhanced address-status codes
	// that unambiguously describe the destination are suppression-authoritative.
	fields := strings.FieldsSeq(protocolErr.Msg)
	for field := range fields {
		code := strings.Trim(field, "[]():;,.")
		switch code {
		case "5.1.1", "5.1.2", "5.1.3", "5.1.4", "5.1.6":
			return true
		}
	}
	return false
}

// sendWithConn sends an email using an existing SMTP connection.
func (a *SMTPAdapter) sendWithConn(
	ctx context.Context,
	sc *smtpConn,
	email *Email,
	msg []byte,
) error {
	if sc == nil {
		return fmt.Errorf("nil smtp connection")
	}

	return withSMTPConnContext(ctx, sc.conn, func() error {
		fromEmail, err := mailboxAddress(a.from)
		if err != nil {
			return err
		}
		toEmail, err := mailboxAddress(email.To)
		if err != nil {
			return err
		}
		if err := sc.client.Mail(fromEmail); err != nil {
			return fmt.Errorf("MAIL FROM failed: %w", err)
		}

		if err := sc.client.Rcpt(toEmail); err != nil {
			return fmt.Errorf("RCPT TO failed: %w", err)
		}

		w, err := sc.client.Data()
		if err != nil {
			return fmt.Errorf("DATA failed: %w", err)
		}

		if _, err := w.Write(msg); err != nil {
			return fmt.Errorf("failed to write message: %w", err)
		}

		if err := w.Close(); err != nil {
			return fmt.Errorf("failed to close message: %w", err)
		}

		return nil
	})
}

// acquireConn gets a connection from the pool or creates a new one.
func (a *SMTPAdapter) acquireConn(ctx context.Context) (*smtpConn, error) {
	for {
		if a.closed.Load() {
			return nil, NewDeliveryError(DeliveryErrorConnection, true, fmt.Errorf("SMTP adapter is closed"))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case sc := <-a.pool:
			// Check if connection is too old
			if time.Since(sc.created) > smtpMaxConnAge {
				a.closeConn(sc)
				continue
			}
			// Quick liveness check via NOOP
			if err := withSMTPConnContext(ctx, sc.conn, sc.client.Noop); err != nil {
				a.closeConn(sc)
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				continue
			}
			return sc, nil
		default:
			// Pool empty, create new connection
			sc, err := a.newConn(ctx)
			if err != nil {
				slog.Error("Failed to create SMTP connection",
					"adapter_id", a.id,
					"error", err,
				)
				return nil, err
			}
			if a.closed.Load() {
				a.closeConn(sc)
				return nil, NewDeliveryError(DeliveryErrorConnection, true, fmt.Errorf("SMTP adapter is closed"))
			}
			return sc, nil
		}
	}
}

// releaseConn returns a connection to the pool or closes it if pool is full.
func (a *SMTPAdapter) releaseConn(sc *smtpConn) {
	if sc == nil {
		return
	}
	if a.closed.Load() {
		a.closeConn(sc)
		return
	}
	select {
	case a.pool <- sc:
		// Returned to pool
	default:
		// Pool full, close connection
		a.closeConn(sc)
	}
}

// newConn creates a new authenticated SMTP connection.
func (a *SMTPAdapter) newConn(ctx context.Context) (*smtpConn, error) {
	addr := net.JoinHostPort(a.host, fmt.Sprintf("%d", a.port))
	dialer := &net.Dialer{Timeout: smtpConnectTimeout}

	conn, err := a.dialSMTPConnection(ctx, dialer, addr)
	if err != nil {
		return nil, err
	}

	var client *smtp.Client
	err = withSMTPConnContext(ctx, conn, func() error {
		var clientErr error
		client, clientErr = smtp.NewClient(conn, a.host)
		return clientErr
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create SMTP client: %w", err)
	}

	if err := a.startTLSIfAvailable(ctx, conn, client); err != nil {
		client.Close()
		conn.Close()
		return nil, err
	}

	if a.user != "" && a.password != "" {
		auth := smtp.PlainAuth("", a.user, a.password, a.host)
		if err := withSMTPConnContext(ctx, conn, func() error {
			return client.Auth(auth)
		}); err != nil {
			client.Close()
			conn.Close()
			return nil, fmt.Errorf("authentication failed: %w", err)
		}
	}

	return &smtpConn{
		client:  client,
		conn:    conn,
		created: time.Now(),
	}, nil
}

func (a *SMTPAdapter) dialSMTPConnection(ctx context.Context, dialer *net.Dialer, addr string) (net.Conn, error) {
	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	if !a.secure {
		return rawConn, nil
	}

	tlsConn := tls.Client(rawConn, &tls.Config{ServerName: a.host})
	if err := withSMTPConnContext(ctx, tlsConn, func() error {
		return tlsConn.HandshakeContext(ctx)
	}); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}
	return tlsConn, nil
}

func (a *SMTPAdapter) startTLSIfAvailable(ctx context.Context, conn net.Conn, client *smtp.Client) error {
	if a.secure {
		return nil
	}
	ok, _ := client.Extension("STARTTLS")
	if !ok {
		return nil
	}
	if err := withSMTPConnContext(ctx, conn, func() error {
		return client.StartTLS(&tls.Config{ServerName: a.host})
	}); err != nil {
		return fmt.Errorf("STARTTLS failed: %w", err)
	}
	return nil
}

// closeConn safely closes an SMTP connection.
func (a *SMTPAdapter) closeConn(sc *smtpConn) {
	if sc == nil {
		return
	}
	if sc.conn != nil {
		_ = sc.conn.Close()
	}
	if sc.client != nil {
		_ = sc.client.Close()
	}
}

func withSMTPConnContext(
	ctx context.Context,
	conn net.Conn,
	operation func() error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	deadline := time.Now().Add(smtpIOTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok &&
		contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}

	cancelWatchDone := make(chan struct{})
	var cancelWatch sync.WaitGroup
	cancelWatch.Go(func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-cancelWatchDone:
		}
	})

	err := operation()
	close(cancelWatchDone)
	cancelWatch.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		if contextDeadline, ok := ctx.Deadline(); ok &&
			!time.Now().Before(contextDeadline) {
			return context.DeadlineExceeded
		}
		return err
	}
	if clearErr := conn.SetDeadline(time.Time{}); clearErr != nil {
		return clearErr
	}
	return nil
}

// buildMessage constructs an RFC-oriented MIME message without allowing raw
// user/template values to create additional headers.
func (a *SMTPAdapter) buildMessage(email *Email) ([]byte, error) {
	if email == nil {
		return nil, NewDeliveryError(DeliveryErrorTemplate, false, fmt.Errorf("email is required"))
	}
	from, err := mail.ParseAddress(a.from)
	if err != nil {
		return nil, NewDeliveryError(DeliveryErrorAuthentication, false, fmt.Errorf("invalid SMTP sender: %w", err))
	}
	to, err := mail.ParseAddress(strings.TrimSpace(email.To))
	if err != nil {
		return nil, NewDeliveryError(DeliveryErrorInvalidRecipient, false, fmt.Errorf("invalid SMTP recipient: %w", err))
	}
	if err := rejectHeaderNewlines(email.Subject, email.MessageID); err != nil {
		return nil, NewDeliveryError(DeliveryErrorTemplate, false, err)
	}

	contentType, body, err := buildMIMEBody(email)
	if err != nil {
		return nil, NewDeliveryError(DeliveryErrorTemplate, false, err)
	}

	var message bytes.Buffer
	writeMIMEHeader(&message, "From", from.String())
	writeMIMEHeader(&message, "To", to.String())
	if strings.TrimSpace(email.MessageID) != "" {
		writeMIMEHeader(&message, "Message-ID", canonicalSMTPMessageID(email.MessageID, from.Address))
	}
	writeMIMEHeader(&message, "Subject", mime.QEncoding.Encode("utf-8", email.Subject))
	writeMIMEHeader(&message, "MIME-Version", "1.0")
	writeMIMEHeader(&message, "Content-Type", contentType)
	if !strings.HasPrefix(contentType, "multipart/") {
		writeMIMEHeader(&message, "Content-Transfer-Encoding", "quoted-printable")
	}
	message.WriteString("\r\n")
	message.Write(body)
	return message.Bytes(), nil
}

func buildMIMEBody(email *Email) (string, []byte, error) {
	if email.HTML != "" && email.Text != "" {
		return buildMultipartAlternativeBody(email.Text, email.HTML)
	}
	mediaType := "text/plain"
	content := email.Text
	if email.HTML != "" {
		mediaType = "text/html"
		content = email.HTML
	}
	var body bytes.Buffer
	qp := quotedprintable.NewWriter(&body)
	if _, err := qp.Write([]byte(normalizeCRLF(content))); err != nil {
		return "", nil, err
	}
	if err := qp.Close(); err != nil {
		return "", nil, err
	}
	return mediaType + `; charset="UTF-8"`, body.Bytes(), nil
}

func buildMultipartAlternativeBody(textContent, htmlContent string) (string, []byte, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	contentType := fmt.Sprintf("multipart/alternative; boundary=%q", writer.Boundary())
	if err := writeQuotedPrintablePart(writer, "text/plain", textContent); err != nil {
		return "", nil, err
	}
	if err := writeQuotedPrintablePart(writer, "text/html", htmlContent); err != nil {
		return "", nil, err
	}
	if err := writer.Close(); err != nil {
		return "", nil, err
	}
	return contentType, body.Bytes(), nil
}

func writeQuotedPrintablePart(writer *multipart.Writer, mediaType string, content string) error {
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", mediaType+`; charset="UTF-8"`)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(part)
	if _, err := qp.Write([]byte(normalizeCRLF(content))); err != nil {
		return err
	}
	return qp.Close()
}

func rejectHeaderNewlines(values ...string) error {
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("SMTP header value contains a newline")
		}
	}
	return nil
}

func writeMIMEHeader(dst *bytes.Buffer, name string, value string) {
	dst.WriteString(name)
	dst.WriteString(": ")
	dst.WriteString(value)
	dst.WriteString("\r\n")
}

func canonicalSMTPMessageID(value, sender string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	domain := "localhost"
	trimmedSender := strings.TrimSpace(sender)
	if at := strings.LastIndex(trimmedSender, "@"); at >= 0 && at+1 < len(trimmedSender) {
		domain = trimmedSender[at+1:]
	}
	return "<" + hex.EncodeToString(sum[:]) + "@" + domain + ">"
}

func mailboxAddress(value string) (string, error) {
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	return address.Address, nil
}

func normalizeCRLF(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}
