package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"time"
)

// smtpConn is the sending side of an SMTP session. It exists because the
// go-smtp client greets with "EHLO localhost" before STARTTLS, which receivers
// count against the sender.
type smtpConn struct {
	conn    net.Conn
	text    *textproto.Conn
	exts    map[string]string
	timeout time.Duration
}

// replyError is a reply from the server.
type replyError struct {
	Code int
	Msg  string
}

func (e *replyError) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Msg) }

func newSMTPConn(conn net.Conn, timeout time.Duration) (*smtpConn, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	c := &smtpConn{conn: conn, text: textproto.NewConn(conn), timeout: timeout}
	c.deadline()
	if _, err := c.read(220); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *smtpConn) Close() error { return c.conn.Close() }

func (c *smtpConn) deadline() { _ = c.conn.SetDeadline(time.Now().Add(c.timeout)) }

func (c *smtpConn) read(want int) (string, error) {
	code, msg, err := c.text.ReadResponse(want)
	if err != nil {
		var te *textproto.Error
		if errors.As(err, &te) {
			return "", &replyError{Code: te.Code, Msg: te.Msg}
		}
		if code != 0 {
			return "", &replyError{Code: code, Msg: msg}
		}
		return "", err
	}
	return msg, nil
}

func (c *smtpConn) cmd(want int, format string, args ...any) (string, error) {
	c.deadline()
	if err := c.text.PrintfLine(format, args...); err != nil {
		return "", err
	}
	return c.read(want)
}

// hello sends EHLO, falling back to HELO, and records the extensions.
func (c *smtpConn) hello(name string) error {
	if strings.ContainsAny(name, "\r\n ") || name == "" {
		return fmt.Errorf("bad host name %q", name)
	}
	msg, err := c.cmd(250, "EHLO %s", name)
	if err != nil {
		var re *replyError
		if errors.As(err, &re) && (re.Code == 500 || re.Code == 502) {
			c.exts = map[string]string{}
			_, err = c.cmd(250, "HELO %s", name)
		}
		return err
	}
	c.exts = map[string]string{}
	lines := strings.Split(msg, "\n")
	if len(lines) > 1 {
		for _, l := range lines[1:] {
			k, v, _ := strings.Cut(strings.TrimSpace(l), " ")
			c.exts[strings.ToUpper(k)] = v
		}
	}
	return nil
}

func (c *smtpConn) has(ext string) bool { _, ok := c.exts[ext]; return ok }

// startTLS upgrades the connection; the caller says hello again.
func (c *smtpConn) startTLS(cfg *tls.Config) error {
	if _, err := c.cmd(220, "STARTTLS"); err != nil {
		return err
	}
	tc := tls.Client(c.conn, cfg)
	c.deadline()
	if err := tc.Handshake(); err != nil {
		return err
	}
	c.conn, c.text = tc, textproto.NewConn(tc)
	return nil
}

func (c *smtpConn) authPlain(user, pass string) error {
	resp := base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
	_, err := c.cmd(235, "AUTH PLAIN %s", resp)
	return err
}

// send runs one transaction. raw uses CRLF line endings.
func (c *smtpConn) send(from, to string, raw []byte) error {
	for _, a := range []string{from, to} {
		if strings.ContainsAny(a, "\r\n<>") {
			return fmt.Errorf("bad address %q", a)
		}
	}
	// Addresses are ASCII: accounts.CheckEmail admits nothing else.
	if _, err := c.cmd(250, "MAIL FROM:<%s>", from); err != nil {
		return err
	}
	if _, err := c.cmd(25, "RCPT TO:<%s>", to); err != nil { // 250 or 251
		return err
	}
	if _, err := c.cmd(354, "DATA"); err != nil {
		return err
	}
	c.deadline()
	w := c.text.DotWriter() // dot-stuffs, and turns \n into \r\n without doubling \r\n
	if _, err := w.Write(raw); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if _, err := c.read(250); err != nil {
		return err
	}
	c.deadline()
	_ = c.text.PrintfLine("QUIT")
	return nil
}
