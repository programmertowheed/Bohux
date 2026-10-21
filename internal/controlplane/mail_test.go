package controlplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMailRelay(t *testing.T) {
	pw := filepath.Join(t.TempDir(), "smtp-password")
	if err := os.WriteFile(pw, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		cfg      MailConfig
		password string
		wantErr  string
	}{
		{name: "none", cfg: MailConfig{}},
		{name: "password from the env value", cfg: MailConfig{Addr: "mail.example.com:587", From: "b@example.com",
			Username: "u", Password: "from-env"}, password: "from-env"},
		{name: "the file wins", cfg: MailConfig{Addr: "mail.example.com:465", From: "b@example.com",
			Username: "u", Password: "from-env", PasswordFile: pw}, password: "from-file"},
		{name: "no sender", cfg: MailConfig{Addr: "mail.example.com:587"}, wantErr: "sender"},
		{name: "no port", cfg: MailConfig{Addr: "mail.example.com", From: "b@example.com"}, wantErr: "address"},
		{name: "user without password", cfg: MailConfig{Addr: "m:587", From: "b@example.com", Username: "u"}, wantErr: "password"},
		{name: "unreadable file", cfg: MailConfig{Addr: "m:587", From: "b@example.com", PasswordFile: pw + ".missing"},
			wantErr: "password file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := tt.cfg.relay()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s.Enabled() != (tt.cfg.Addr != "") || s.Password != tt.password {
				t.Errorf("relay = %+v", s)
			}
		})
	}
}
