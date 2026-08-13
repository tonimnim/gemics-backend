package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"strconv"
	"time"
)

type Sender interface {
	SendOTP(context.Context, string, string) error
}

type LogSender struct{ Logger *slog.Logger }

func (s LogSender) SendOTP(_ context.Context, recipient, code string) error {
	s.Logger.Warn("development OTP email", "recipient", recipient, "code", code)
	return nil
}

type SMTPSender struct {
	Host, Username, Password, From string
	Port                           int
	Timeout                        time.Duration
	RequireTLS                     bool
}

func (s SMTPSender) SendOTP(ctx context.Context, recipient, code string) error {
	from, err := stdmail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("parse SMTP from address: %w", err)
	}
	to, err := stdmail.ParseAddress(recipient)
	if err != nil || to.Address != recipient {
		return errors.New("invalid SMTP recipient")
	}
	if s.Timeout <= 0 {
		s.Timeout = 10 * time.Second
	}
	dialer := net.Dialer{Timeout: minDuration(s.Timeout, 5*time.Second), KeepAlive: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline := time.Now().Add(s.Timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	cancelWatch := make(chan struct{})
	defer close(cancelWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-cancelWatch:
		}
	}()

	client, err := smtp.NewClient(connection, s.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if supported, _ := client.Extension("STARTTLS"); supported {
		if err := client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else if s.RequireTLS {
		return errors.New("SMTP server does not support STARTTLS")
	}
	if s.Username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("SMTP server does not support authentication")
		}
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	if err := client.Rcpt(to.Address); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := []byte("From: " + from.String() + "\r\n" +
		"To: " + to.String() + "\r\n" +
		"Subject: Your Gamics sign-in code\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		"Your Gamics code is " + code + ". It expires soon. If you did not request it, ignore this email.\r\n")
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
