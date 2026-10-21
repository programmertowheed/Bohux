package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// MaxRecipients bounds an EMAIL channel's mailto: list (ADR-0043).
const MaxRecipients = 10

// ErrPermanent marks a refusal that retrying cannot change: the relay answered 5xx.
var ErrPermanent = errors.New("permanent SMTP refusal")

// classify wraps a relay's 5xx reply as ErrPermanent.
func classify(step string, err error) error {
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 500 {
		return fmt.Errorf("send email: %s: %w: %w", step, ErrPermanent, err)
	}
	return fmt.Errorf("send email: %s: %w", step, err)
}

// SMTP is the install's mail relay (ADR-0043). The zero value sends no mail.
type SMTP struct {
	// Addr is host:port; port 465 is implicit TLS, any other must offer STARTTLS.
	Addr     string
	From     string
	Username string
	Password string
	// ImplicitTLS speaks TLS from the first byte; port 465 implies it.
	ImplicitTLS bool
	// TLS, if set, replaces the default client config (system roots, ServerName from
	// Addr); tests use it to trust their relay.
	TLS *tls.Config
}

// Enabled reports whether a relay is configured.
func (s SMTP) Enabled() bool { return s.Addr != "" }

// Recipients parses a channel's recipients: a mailto: URL, or the same addresses as a
// plain comma-separated list. One to MaxRecipients plain addresses, no display names
// and no headers. Its errors never quote the input.
func Recipients(raw string) ([]string, error) {
	bad := errors.New("want mailto:address[,address…] or address[,address…], without headers")
	raw = strings.TrimSpace(raw)
	if len(raw) >= len("mailto:") && strings.EqualFold(raw[:len("mailto:")], "mailto:") {
		raw = raw[len("mailto:"):]
	}
	if raw == "" || strings.ContainsAny(raw, "?#") {
		return nil, bad
	}
	list, err := url.PathUnescape(raw)
	if err != nil {
		return nil, bad
	}
	list = strings.TrimSuffix(strings.TrimSpace(list), ",")
	parts := strings.Split(list, ",")
	if len(parts) > MaxRecipients {
		return nil, fmt.Errorf("at most %d recipients", MaxRecipients)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		a, err := mail.ParseAddress(p)
		if err != nil || a.Name != "" || a.Address != p || strings.ContainsAny(p, "\r\n<>") {
			return nil, errors.New("every recipient must be a plain address, like ops@example.com")
		}
		out = append(out, a.Address)
	}
	return out, nil
}

// MailtoURL is the canonical form of a recipient list, as the API stores it.
func MailtoURL(addrs []string) string { return "mailto:" + strings.Join(addrs, ",") }

// Domains is what the API shows of a mailto: URL: its recipients' domains.
func Domains(addrs []string) string {
	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		d := a[strings.LastIndex(a, "@")+1:]
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, ",")
}

// Subject is the message's subject line.
func (o Outcome) Subject() string {
	return fmt.Sprintf("[Bohux] %s/%s/%s: deployment %s", o.Project, o.Environment, o.App,
		strings.ReplaceAll(o.State, "_", " "))
}

// Body is the plain-text message.
func (o Outcome) Body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Deployment %s of %s/%s/%s ended %s.\r\n\r\n", o.DeploymentID, o.Project, o.Environment, o.App,
		strings.ReplaceAll(o.State, "_", " "))
	fmt.Fprintf(&b, "Release:    %s\r\n", o.ReleaseID)
	if o.Reason != "" {
		fmt.Fprintf(&b, "Reason:     %s\r\n", o.Reason)
	}
	fmt.Fprintf(&b, "Started by: %s\r\n", o.StartedBy)
	return b.String()
}

// oneLine keeps a value from adding header lines.
func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// message renders the whole mail, headers first.
func (s SMTP) message(to []string, o Outcome, now time.Time) []byte {
	var b strings.Builder
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, oneLine(v)) }
	header("From", s.From)
	header("To", strings.Join(to, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", o.Subject()))
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("Message-ID", fmt.Sprintf("<%s.%s@bohux>", o.DeploymentID, o.State))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "8bit")
	header("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	b.WriteString(o.Body())
	return []byte(b.String())
}

// SendEmail mails o to the recipients through the relay, over TLS only. Its errors
// name the step that failed, never the password or the recipients.
func SendEmail(ctx context.Context, s SMTP, to []string, o Outcome) error {
	if !s.Enabled() {
		return errors.New("send email: this install has no SMTP relay")
	}
	host, port, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return fmt.Errorf("send email: relay address: %w", err)
	}
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if s.TLS != nil {
		cfg = s.TLS.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("send email: connect to relay: %w", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(time.Minute)
	}
	_ = conn.SetDeadline(deadline)
	implicit := s.ImplicitTLS || port == "465"
	if implicit {
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("send email: TLS: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("send email: greeting: %w", err)
	}
	defer func() { _ = c.Close() }()
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("send email: the relay does not offer STARTTLS; refusing to send in clear")
		}
		if err := c.StartTLS(cfg); err != nil {
			return fmt.Errorf("send email: STARTTLS: %w", err)
		}
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); err != nil {
			return classify("authenticate", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return classify("sender", err)
	}
	for i := range to {
		if err := c.Rcpt(to[i]); err != nil {
			return classify(fmt.Sprintf("recipient %d of %d refused", i+1, len(to)), err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return classify("data", err)
	}
	if _, err := w.Write(s.message(to, o, time.Now())); err != nil {
		_ = w.Close()
		return fmt.Errorf("send email: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return classify("relay refused the message", err)
	}
	// The relay has the message; a failed goodbye must not send it again.
	_ = c.Quit()
	return nil
}
