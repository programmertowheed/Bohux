package notify_test

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/bohumix/bohux/internal/notify"
	"github.com/bohumix/bohux/internal/notify/smtptest"
)

func TestRecipients(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "mailto:ops@example.com", want: "ops@example.com"},
		{raw: "mailto:ops@example.com,dev@example.org", want: "ops@example.com dev@example.org"},
		{raw: " ops@example.com, dev@example.org, ", want: "ops@example.com dev@example.org"},
		{raw: "MAILTO:ops@example.com", want: "ops@example.com"},
		{raw: "mailto:ops@example.com?subject=x", wantErr: true},
		{raw: "mailto:Ops%20%3Cops@example.com%3E", wantErr: true},
		{raw: "mailto:ops@example.com%0D%0ABcc:x@evil.example", wantErr: true},
		{raw: "https://example.com", wantErr: true},
		{raw: "mailto:", wantErr: true},
		{raw: "mailto:" + strings.Repeat("a@example.com,", notify.MaxRecipients) + "b@example.com", wantErr: true},
	}
	for _, tt := range tests {
		got, err := notify.Recipients(tt.raw)
		if (err != nil) != tt.wantErr || strings.Join(got, " ") != tt.want {
			t.Errorf("Recipients(%q) = %v, %v", tt.raw, got, err)
		}
		if err != nil && (strings.Contains(err.Error(), "evil") || strings.Contains(err.Error(), "Ops") ||
			strings.Contains(err.Error(), "subject")) {
			t.Errorf("error quotes the input: %v", err)
		}
	}
	if got := notify.MailtoURL([]string{"a@x.example", "b@y.example"}); got != "mailto:a@x.example,b@y.example" {
		t.Errorf("MailtoURL = %q", got)
	}
	if d := notify.Domains([]string{"a@x.example", "b@x.example", "c@y.example"}); d != "x.example,y.example" {
		t.Errorf("Domains = %q", d)
	}
}

func TestSendEmail(t *testing.T) {
	o := notify.Outcome{DeploymentID: "dep-1", Project: "shop", Environment: "prod", App: "api", ReleaseID: "rel-1",
		State: "rolled_back", Reason: "HEALTH_TIMEOUT", StartedBy: "webhook"}
	to := []string{"ops@example.com", "dev@example.com"}
	relay := func(r *smtptest.Relay) notify.SMTP {
		return notify.SMTP{Addr: r.Addr, From: "bohux@example.com", Username: "bohux", Password: "relay-sekrit",
			TLS: &tls.Config{RootCAs: r.Roots}}
	}

	t.Run("STARTTLS before anything else, then auth and the message", func(t *testing.T) {
		r := smtptest.Start(t, smtptest.StartTLS)
		if err := notify.SendEmail(context.Background(), relay(r), to, o); err != nil {
			t.Fatal(err)
		}
		seen := r.Seen()
		if strings.Join(seen.ClearCmds, ",") != "EHLO,STARTTLS" {
			t.Errorf("commands in clear: %v", seen.ClearCmds)
		}
		cred, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(seen.Auth, "AUTH PLAIN "))
		if string(cred) != "\x00bohux\x00relay-sekrit" {
			t.Errorf("auth %q", seen.Auth)
		}
		if !strings.HasPrefix(seen.From, "MAIL FROM:<bohux@example.com>") || len(seen.Rcpt) != 2 {
			t.Errorf("from %q, rcpt %v", seen.From, seen.Rcpt)
		}
		for _, want := range []string{"Subject: [Bohux] shop/prod/api: deployment rolled back\r\n",
			"To: ops@example.com, dev@example.com\r\n", "Reason:     HEALTH_TIMEOUT", "Auto-Submitted: auto-generated"} {
			if !strings.Contains(seen.Data, want) {
				t.Errorf("message lacks %q:\n%s", want, seen.Data)
			}
		}
	})

	t.Run("implicit TLS", func(t *testing.T) {
		r := smtptest.Start(t, smtptest.Implicit)
		s := relay(r)
		s.ImplicitTLS = true
		if err := notify.SendEmail(context.Background(), s, to[:1], o); err != nil {
			t.Fatal(err)
		}
		if seen := r.Seen(); len(seen.ClearCmds) != 0 || !strings.Contains(seen.Data, "Deployment dep-1") {
			t.Errorf("clear %v, data %q", seen.ClearCmds, seen.Data)
		}
	})

	t.Run("a relay without STARTTLS gets no credentials", func(t *testing.T) {
		r := smtptest.Start(t, smtptest.Plain)
		err := notify.SendEmail(context.Background(), relay(r), to, o)
		if err == nil || !strings.Contains(err.Error(), "STARTTLS") || strings.Contains(err.Error(), "sekrit") {
			t.Fatalf("err = %v, want a refusal without the password", err)
		}
		if seen := r.Seen(); seen.Auth != "" || seen.Data != "" {
			t.Errorf("sent in clear: auth %q, data %q", seen.Auth, seen.Data)
		}
	})

	t.Run("an untrusted certificate fails before auth", func(t *testing.T) {
		r := smtptest.Start(t, smtptest.StartTLS)
		s := relay(r)
		s.TLS = nil // system roots do not trust the test relay
		if err := notify.SendEmail(context.Background(), s, to, o); err == nil {
			t.Fatal("sent to a relay with an untrusted certificate")
		}
		if r.Seen().Auth != "" {
			t.Error("authenticated to an untrusted relay")
		}
	})

	t.Run("a dropped goodbye after the relay took the message is a success", func(t *testing.T) {
		r := smtptest.StartWith(t, smtptest.StartTLS, smtptest.Options{DropOnQuit: true})
		if err := notify.SendEmail(context.Background(), relay(r), to, o); err != nil {
			t.Fatalf("err = %v; a retry would send the message twice", err)
		}
		if r.Seen().Data == "" {
			t.Error("the relay did not get the message")
		}
	})

	t.Run("a 5xx refusal is permanent", func(t *testing.T) {
		r := smtptest.StartWith(t, smtptest.StartTLS, smtptest.Options{RefuseRcpt: true})
		err := notify.SendEmail(context.Background(), relay(r), to, o)
		if !errors.Is(err, notify.ErrPermanent) || strings.Contains(err.Error(), "ops@example.com") {
			t.Fatalf("err = %v, want ErrPermanent without the address", err)
		}
	})

	t.Run("no relay configured", func(t *testing.T) {
		if err := notify.SendEmail(context.Background(), notify.SMTP{}, to, o); err == nil {
			t.Error("sent without a relay")
		}
	})
}
