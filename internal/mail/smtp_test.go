package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
)

func TestSMTPSendsVerificationOverTLS(t *testing.T) {
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
			defer certificateServer.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverTLS := certificateServer.TLS.Clone()
			if mode == "tls" {
				listener = tls.NewListener(listener, serverTLS)
			}
			type result struct {
				message []byte
				err     error
			}
			results := make(chan result, 1)
			go func() {
				message, err := receiveSMTP(listener, serverTLS, mode == "tls")
				results <- result{message, err}
			}()
			_, port, _ := net.SplitHostPort(listener.Addr().String())
			portNumber, _ := strconv.Atoi(port)
			mailer, err := NewSMTPMailer(config.SMTPConfig{
				Enabled: true, Host: "127.0.0.1", Port: portNumber, TLSMode: mode,
				Username: "sender@yandex.ru", Password: "test-application-password", FromEmail: "sender@yandex.ru",
				FromName: "Платформа аренды", Timeout: 3 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			roots.AddCert(certificateServer.Certificate())
			mailer.tlsConfig.RootCAs = roots
			email := model.VerificationEmail{
				To: "recipient@mail.ru", FullName: `<img src=x onerror=alert(1)>`, Code: "001234",
				URL: "https://rental.example/auth/verify-email#token=" + strings.Repeat("a", 64), ValidMinutes: 15,
			}
			if err := mailer.SendVerification(t.Context(), email); err != nil {
				t.Fatal(err)
			}
			received := <-results
			if received.err != nil {
				t.Fatal(received.err)
			}
			message, err := netmail.ReadMessage(bytes.NewReader(received.message))
			if err != nil {
				t.Fatal(err)
			}
			subject, err := (&mime.WordDecoder{}).DecodeHeader(message.Header.Get("Subject"))
			if err != nil || subject != "Подтвердите вашу почту" || message.Header.Get("Message-ID") == "" {
				t.Fatal("missing or incorrectly encoded MIME headers")
			}
			mediaType, parameters, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
			if err != nil || mediaType != "multipart/alternative" {
				t.Fatal("verification email must have plain text and HTML alternatives")
			}
			parts := multipart.NewReader(message.Body, parameters["boundary"])
			for _, expectedType := range []string{"text/plain", "text/html"} {
				part, err := parts.NextPart()
				if err != nil {
					t.Fatal(err)
				}
				partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
				body, err := io.ReadAll(part)
				if err != nil || partType != expectedType || !bytes.Contains(body, []byte(email.Code)) || !bytes.Contains(body, []byte(email.URL)) {
					t.Fatalf("invalid %s email alternative", expectedType)
				}
				if expectedType == "text/html" && (bytes.Contains(body, []byte("<img")) || !bytes.Contains(body, []byte("&lt;img"))) {
					t.Fatal("user-supplied names must be escaped in the HTML template")
				}
			}
			if _, err := parts.NextPart(); err != io.EOF {
				t.Fatal("unexpected email MIME parts")
			}
		})
	}
}

func TestSMTPCancellationDoesNotHang(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		close(accepted)
		// Never send an SMTP greeting; a cancelled request must close the socket.
		io.Copy(io.Discard, connection)
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	mailer, err := NewSMTPMailer(config.SMTPConfig{
		Enabled: true, Host: "127.0.0.1", Port: portNumber, TLSMode: "starttls", Timeout: 30 * time.Second,
		Username: "sender@yandex.ru", Password: "test-password", FromEmail: "sender@yandex.ru",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- mailer.SendVerification(ctx, model.VerificationEmail{To: "user@mail.ru", Code: "123456", URL: "https://example.test", ValidMinutes: 15})
	}()
	<-accepted
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled SMTP delivery must fail")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP did not stop after context cancellation")
	}
	<-done
}

func receiveSMTP(listener net.Listener, serverTLS *tls.Config, encrypted bool) ([]byte, error) {
	connection, err := listener.Accept()
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(connection)
	write := func(message string) error {
		_, err := io.WriteString(connection, message)
		return err
	}
	if err := write("220 localhost SMTP test\r\n"); err != nil {
		return nil, err
	}
	var message []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "EHLO"):
			if err := write("250-localhost\r\n250-AUTH PLAIN\r\n250 STARTTLS\r\n"); err != nil {
				return nil, err
			}
		case line == "STARTTLS":
			if err := write("220 ready for TLS\r\n"); err != nil {
				return nil, err
			}
			secure := tls.Server(connection, serverTLS)
			if err := secure.Handshake(); err != nil {
				return nil, err
			}
			connection, reader, encrypted = secure, bufio.NewReader(secure), true
		case strings.HasPrefix(line, "AUTH PLAIN "):
			if !encrypted {
				return nil, fmt.Errorf("SMTP authentication attempted without TLS")
			}
			credentials, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			if err != nil || string(credentials) != "\x00sender@yandex.ru\x00test-application-password" {
				return nil, fmt.Errorf("incorrect SMTP authentication")
			}
			write("235 authenticated\r\n")
		case line == "MAIL FROM:<sender@yandex.ru>":
			write("250 sender accepted\r\n")
		case line == "RCPT TO:<recipient@mail.ru>":
			write("250 recipient accepted\r\n")
		case line == "DATA":
			write("354 send message\r\n")
			message, err = textproto.NewReader(reader).ReadDotBytes()
			if err != nil {
				return nil, err
			}
			write("250 message accepted\r\n")
		case line == "QUIT":
			// Close without replying to verify that accepted DATA remains successful.
			return message, nil
		default:
			return nil, fmt.Errorf("unexpected SMTP command: %s", line)
		}
	}
}
