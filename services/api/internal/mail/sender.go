package mail

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"
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
}

func (s SMTPSender) SendOTP(_ context.Context, recipient, code string) error {
	address := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.Username != "" {
		auth = smtp.PlainAuth("", s.Username, s.Password, s.Host)
	}
	message := []byte("From: " + s.From + "\r\n" +
		"To: " + recipient + "\r\n" +
		"Subject: Your Gamics sign-in code\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		"Your Gamics code is " + code + ". It expires soon. If you did not request it, ignore this email.\r\n")
	return smtp.SendMail(address, auth, s.From, []string{recipient}, message)
}
