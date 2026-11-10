package service

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
)

func TestSendTestEmailRejectsUntrustedSMTPS(t *testing.T) {
	certificate := httptest.NewTLSServer(http.NotFoundHandler())
	defer certificate.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:465")
	if err != nil {
		t.Skipf("SMTPS integration test requires an available local port 465: %v", err)
	}
	defer listener.Close()
	result := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		server := tls.Server(conn, certificate.TLS)
		if err := server.Handshake(); err != nil {
			result <- nil
			return
		}
		// An insecure client would complete TLS and receive the SMTP greeting.
		_, _ = io.WriteString(server, "554 untrusted test server\r\n")
		result <- errors.New("client accepted the untrusted SMTP certificate")
	}()
	err = (&SystemConfigService{}).SendTestEmail(&dto.EmailConfigReq{
		Host: "127.0.0.1", Port: 465, Username: "test-user", Password: "test-password", FromEmail: "sender@example.test",
	})
	requireServiceError(t, err, bizerrors.ErrSMTPConnectFailed)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestSMTPSMessageAndConnectionLifecycle(t *testing.T) {
	certificate := httptest.NewTLSServer(http.NotFoundHandler())
	defer certificate.Close()
	roots := x509.NewCertPool()
	roots.AddCert(certificate.Certificate())
	for _, test := range []struct {
		name, failure string
		noAuth        bool
		want          error
	}{
		{name: "authenticated delivery"},
		{name: "delivery without credentials", noAuth: true},
		{name: "rejected greeting closes connection", failure: "greeting", want: bizerrors.ErrSMTPClientFailed},
		{name: "rejected authentication", failure: "AUTH", want: bizerrors.ErrSMTPAuthFailed},
		{name: "rejected sender", failure: "MAIL", want: bizerrors.ErrSMTPSenderFailed},
		{name: "rejected recipient", failure: "RCPT", want: bizerrors.ErrSMTPRecipientFailed},
		{name: "rejected data", failure: "DATA", want: bizerrors.ErrSMTPDataFailed},
		{name: "rejected content", failure: "body", want: bizerrors.ErrSMTPCloseFailed},
		{name: "rejected quit", failure: "QUIT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := tls.Listen("tcp4", "127.0.0.1:0", certificate.TLS)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			result := make(chan error, 1)
			go func() { result <- receiveSMTPSTestMessage(listener, test.failure, test.noAuth) }()
			conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "127.0.0.1"})
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			req := &dto.EmailConfigReq{Host: "127.0.0.1", Username: "test-user", Password: "test-password", FromEmail: "sender@example.test"}
			if test.noAuth {
				req.Username, req.Password = "", ""
			}
			err = sendSMTPTestMessage(conn, req, "recipient@example.test", []byte("Subject: test\r\n\r\nmessage\r\n"))
			if test.failure == "QUIT" {
				if err == nil || !strings.Contains(err.Error(), "550") {
					t.Fatalf("rejected QUIT returned %v", err)
				}
			} else {
				requireServiceError(t, err, test.want)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func receiveSMTPSTestMessage(listener net.Listener, failure string, noAuth bool) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(conn)
	reject := func() error {
		if _, err := io.WriteString(conn, "550 rejected by test server\r\n"); err != nil {
			return err
		}
		if failure == "AUTH" {
			// net/smtp aborts AUTH before ending the rejected exchange with QUIT.
			if line, err := reader.ReadString('\n'); err != nil || line != "*\r\n" {
				return fmt.Errorf("SMTP authentication abort = %q, %v", line, err)
			}
			if _, err := io.WriteString(conn, "501 authentication aborted\r\n"); err != nil {
				return err
			}
			if line, err := reader.ReadString('\n'); err != nil || line != "QUIT\r\n" {
				return fmt.Errorf("SMTP authentication cleanup = %q, %v", line, err)
			}
			if _, err := io.WriteString(conn, "221 goodbye\r\n"); err != nil {
				return err
			}
		}
		if _, err := reader.ReadByte(); err != io.EOF {
			return fmt.Errorf("SMTP connection was not closed after %s: %v", failure, err)
		}
		return nil
	}
	if failure == "greeting" {
		return reject()
	}
	if _, err := io.WriteString(conn, "220 SMTP ready\r\n"); err != nil {
		return err
	}
	for _, step := range []struct{ command, reply string }{
		{"EHLO", "250-test\r\n250 AUTH PLAIN\r\n"},
		{"AUTH", "235 authenticated\r\n"},
		{"MAIL", "250 sender accepted\r\n"},
		{"RCPT", "250 recipient accepted\r\n"},
		{"DATA", "354 send body\r\n"},
	} {
		if step.command == "AUTH" && noAuth {
			continue
		}
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, step.command) {
			return fmt.Errorf("SMTP command %q, want %s: %v", line, step.command, err)
		}
		if failure == step.command {
			return reject()
		}
		if _, err := io.WriteString(conn, step.reply); err != nil {
			return err
		}
	}
	var message strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line == ".\r\n" {
			break
		}
		message.WriteString(line)
	}
	if !strings.Contains(message.String(), "Subject: test\r\n") {
		return fmt.Errorf("unexpected SMTP message: %q", message.String())
	}
	if failure == "body" {
		return reject()
	}
	if _, err := io.WriteString(conn, "250 delivered\r\n"); err != nil {
		return err
	}
	line, err := reader.ReadString('\n')
	if err != nil || line != "QUIT\r\n" {
		return fmt.Errorf("SMTP QUIT = %q, %v", line, err)
	}
	if failure == "QUIT" {
		return reject()
	}
	_, err = io.WriteString(conn, "221 goodbye\r\n")
	return err
}
