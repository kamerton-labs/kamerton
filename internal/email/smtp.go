// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Sender talks to the SMTP server. The password is passed per call and is
// never part of an error message.
type Sender interface {
	// Check connects and logs in without sending anything.
	Check(ctx context.Context, cfg ServerSettings, password string) error
	// Send sends a plain-text message.
	Send(ctx context.Context, cfg ServerSettings, password string, to []string, subject, body string) error
}

// SMTPSender implements Sender with net/smtp: implicit TLS on port 465,
// otherwise STARTTLS when the server offers it (required to log in).
type SMTPSender struct {
	// Timeout of one whole conversation. Default 20s.
	Timeout time.Duration
	// TLSConfig is for tests; nil uses the system roots.
	TLSConfig *tls.Config
}

func (s *SMTPSender) timeout() time.Duration {
	if s.Timeout == 0 {
		return 20 * time.Second
	}
	return s.Timeout
}

func (s *SMTPSender) tlsConfig(host string) *tls.Config {
	if s.TLSConfig != nil {
		c := s.TLSConfig.Clone()
		if c.ServerName == "" {
			c.ServerName = host
		}
		return c
	}
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

func (s *SMTPSender) dial(ctx context.Context, cfg ServerSettings, password string) (*smtp.Client, error) {
	host, port, err := net.SplitHostPort(cfg.Smarthost)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	d := &net.Dialer{}
	var conn net.Conn
	if port == "465" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: s.tlsConfig(host)}).DialContext(ctx, "tcp", cfg.Smarthost)
	} else {
		conn, err = d.DialContext(ctx, "tcp", cfg.Smarthost)
	}
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(s.timeout()))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := c.Hello("kamerton"); err != nil {
		_ = c.Close()
		return nil, err
	}
	if _, isTLS := conn.(*tls.Conn); !isTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(s.tlsConfig(host)); err != nil {
				_ = c.Close()
				return nil, err
			}
		}
	}
	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			_ = c.Close()
			return nil, errors.New("the server does not accept a login (no AUTH)")
		}
		// PlainAuth refuses to send the password without TLS (except to localhost).
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, password, host)); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	return c, nil
}

// Check implements Sender.
func (s *SMTPSender) Check(ctx context.Context, cfg ServerSettings, password string) error {
	c, err := s.dial(ctx, cfg, password)
	if err != nil {
		return scrub(err, password)
	}
	_ = c.Quit()
	return nil
}

// Send implements Sender.
func (s *SMTPSender) Send(ctx context.Context, cfg ServerSettings, password string, to []string, subject, body string) error {
	c, err := s.dial(ctx, cfg, password)
	if err != nil {
		return scrub(err, password)
	}
	defer func() { _ = c.Close() }()
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return err
	}
	if err := c.Mail(from.Address); err != nil {
		return scrub(err, password)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return scrub(fmt.Errorf("%s: %w", rcpt, err), password)
		}
	}
	w, err := c.Data()
	if err != nil {
		return scrub(err, password)
	}
	if _, err := w.Write(buildMessage(cfg.From, to, subject, body, time.Now())); err != nil {
		return scrub(err, password)
	}
	if err := w.Close(); err != nil {
		return scrub(err, password)
	}
	return c.Quit()
}

func buildMessage(from string, to []string, subject, body string, now time.Time) []byte {
	var b bytes.Buffer
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.Bytes()
}

// scrub makes sure an error never carries the password (servers may echo
// parts of a failed AUTH exchange).
func scrub(err error, password string) error {
	if err == nil || password == "" || !strings.Contains(err.Error(), password) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), password, "***"))
}

// ---- Validation ----

// ValidSmarthost checks host:port.
func ValidSmarthost(v string) error {
	host, port, err := net.SplitHostPort(v)
	if err != nil || host == "" || strings.ContainsAny(host, " \t\r\n/") {
		return fmt.Errorf("%w: enter the server as host:port, for example smtp.example.com:465", ErrInvalidInput)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("%w: the port must be a number from 1 to 65535", ErrInvalidInput)
	}
	return nil
}

// NormalizeAddress returns the bare address (a@b.c) or an error.
func NormalizeAddress(v string) (string, error) {
	v = strings.TrimSpace(v)
	a, err := mail.ParseAddress(v)
	if err != nil || strings.ContainsAny(v, "\r\n") || !strings.Contains(a.Address, "@") || len(a.Address) > 254 {
		return "", fmt.Errorf("%w: %q does not look like an email address", ErrInvalidInput, v)
	}
	at := strings.LastIndex(a.Address, "@")
	if !strings.Contains(a.Address[at+1:], ".") && a.Address[at+1:] != "localhost" {
		return "", fmt.Errorf("%w: %q does not look like an email address", ErrInvalidInput, v)
	}
	return a.Address, nil
}
