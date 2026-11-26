package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
)

//go:embed templates/verification.html templates/verification.txt
var templates embed.FS

var htmlTemplate = template.Must(template.ParseFS(templates, "templates/verification.html"))
var textTemplate = texttemplate.Must(texttemplate.ParseFS(templates, "templates/verification.txt"))

type SMTPMailer struct {
	cfg       config.SMTPConfig
	from      netmail.Address
	tlsConfig *tls.Config
}

func NewSMTPMailer(cfg config.SMTPConfig) (*SMTPMailer, error) {
	m := &SMTPMailer{cfg: cfg}
	if !cfg.Enabled {
		return m, nil
	}
	if cfg.Host == "" || strings.ContainsAny(cfg.Host, "\r\n /:") || cfg.Port < 1 || cfg.Port > 65535 || cfg.Timeout <= 0 {
		return nil, fmt.Errorf("SMTP host, port and positive timeout must be configured")
	}
	if cfg.TLSMode != "tls" && cfg.TLSMode != "starttls" {
		return nil, fmt.Errorf("SMTP_TLS_MODE must be tls (465) or starttls (587)")
	}
	if cfg.Username == "" || cfg.Password == "" || strings.ContainsAny(cfg.Username, "\r\n") || strings.ContainsAny(cfg.FromName, "\r\n") {
		return nil, fmt.Errorf("SMTP username, application password and a valid sender name must be configured")
	}
	if cfg.FromEmail == "" {
		cfg.FromEmail = cfg.Username
	}
	address, err := netmail.ParseAddress(cfg.FromEmail)
	if err != nil || address.Address != cfg.FromEmail || strings.ContainsAny(cfg.FromEmail, "\r\n") {
		return nil, fmt.Errorf("SMTP_FROM_EMAIL must be a valid email address")
	}
	m.cfg = cfg
	m.from = netmail.Address{Name: cfg.FromName, Address: address.Address}
	m.tlsConfig = &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	return m, nil
}

func (m *SMTPMailer) Available() bool {
	return m.cfg.Enabled
}

func (m *SMTPMailer) SendVerification(ctx context.Context, email model.VerificationEmail) error {
	if !m.Available() {
		return model.ErrMailUnavailable
	}
	to, err := netmail.ParseAddress(email.To)
	if err != nil || to.Address != email.To || strings.ContainsAny(email.To, "\r\n") {
		return fmt.Errorf("invalid verification email recipient")
	}
	message, err := m.message(email)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port)))
	if err != nil {
		return fmt.Errorf("connect SMTP: %w", err)
	}
	defer connection.Close()
	rawConnection := connection
	stop := context.AfterFunc(ctx, func() { rawConnection.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("set SMTP deadline: %w", err)
		}
	}
	tlsConfig := m.tlsConfig.Clone()
	if m.cfg.TLSMode == "tls" {
		secure := tls.Client(connection, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("SMTP TLS handshake: %w", err)
		}
		connection = secure
	}
	client, err := smtp.NewClient(connection, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("initialize SMTP client: %w", err)
	}
	defer client.Close()
	if m.cfg.TLSMode == "starttls" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}
	if err := client.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
		return fmt.Errorf("SMTP authentication: %w", err)
	}
	if err := client.Mail(m.from.Address); err != nil {
		return fmt.Errorf("SMTP sender: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		writer.Close()
		return fmt.Errorf("write verification email: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("submit verification email: %w", err)
	}
	// Once DATA is accepted, a QUIT failure must not invalidate a delivered code.
	client.Quit()
	return nil
}

func (m *SMTPMailer) message(email model.VerificationEmail) ([]byte, error) {
	var plain, html bytes.Buffer
	if err := textTemplate.Execute(&plain, email); err != nil {
		return nil, fmt.Errorf("render text email: %w", err)
	}
	if err := htmlTemplate.Execute(&html, email); err != nil {
		return nil, fmt.Errorf("render HTML email: %w", err)
	}
	var body bytes.Buffer
	parts := multipart.NewWriter(&body)
	for _, part := range []struct {
		contentType string
		body        []byte
	}{
		{"text/plain; charset=utf-8", plain.Bytes()},
		{"text/html; charset=utf-8", html.Bytes()},
	} {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", part.contentType)
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		writer, err := parts.CreatePart(header)
		if err != nil {
			return nil, err
		}
		encoded := quotedprintable.NewWriter(writer)
		if _, err := encoded.Write(part.body); err != nil {
			return nil, err
		}
		if err := encoded.Close(); err != nil {
			return nil, err
		}
	}
	if err := parts.Close(); err != nil {
		return nil, err
	}
	var messageID [16]byte
	if _, err := rand.Read(messageID[:]); err != nil {
		return nil, err
	}
	domain := strings.SplitN(m.from.Address, "@", 2)[1]
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\n", m.from.String(), (&netmail.Address{Address: email.To}).String())
	fmt.Fprintf(&message, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", "Подтвердите вашу почту"))
	fmt.Fprintf(&message, "Date: %s\r\nMessage-ID: <%s@%s>\r\n", time.Now().Format(time.RFC1123Z), hex.EncodeToString(messageID[:]), domain)
	fmt.Fprintf(&message, "MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", parts.Boundary())
	message.Write(body.Bytes())
	return message.Bytes(), nil
}
