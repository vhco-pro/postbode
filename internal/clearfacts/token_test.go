package clearfacts

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "PAT sourced through an interface (env in dev, Keychain in prod — Keychain impl lands Phase 13) and redacted to `cf_***` in every log line, error string and `%v` formatting path (F-55)"
func TestTokenRedaction(t *testing.T) {
	const secret = "shhh-do-not-leak-me-1234567890"
	tok := Token(secret)

	tests := []struct {
		name string
		got  string
	}{
		{"String()", tok.String()},
		{"GoString()", tok.GoString()},
		{"%v", fmt.Sprintf("%v", tok)},
		{"%s", fmt.Sprintf("%s", tok)},
		{"%q", fmt.Sprintf("%q", tok)},
		{"%#v", fmt.Sprintf("%#v", tok)},
		{"error wrap with %v", fmt.Errorf("upload failed with token %v", tok).Error()},
		{"error wrap with %w-adjacent %s", fmt.Sprintf("token=%s", tok)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(tt.got, secret) {
				t.Fatalf("formatted output leaked the raw token: %q", tt.got)
			}
			if !strings.Contains(tt.got, redactedToken) {
				t.Errorf("formatted output %q does not contain the redacted form %q", tt.got, redactedToken)
			}
		})
	}
}

func TestTokenReveal(t *testing.T) {
	const secret = "the-real-value"
	tok := Token(secret)
	if got := tok.Reveal(); got != secret {
		t.Errorf("Reveal() = %q, want %q", got, secret)
	}
}

func TestEnvTokenSource(t *testing.T) {
	tests := []struct {
		name    string
		envVar  string
		setEnv  map[string]string
		want    Token
		wantErr bool
	}{
		{name: "default var CF_TOKEN", envVar: "", setEnv: map[string]string{"CF_TOKEN": "abc123"}, want: Token("abc123")},
		{name: "custom var name", envVar: "MY_CF_TOKEN", setEnv: map[string]string{"MY_CF_TOKEN": "xyz"}, want: Token("xyz")},
		{name: "unset variable errors", envVar: "CF_TOKEN_DOES_NOT_EXIST_XYZ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.setEnv {
				t.Setenv(k, v)
			}
			src := EnvTokenSource{EnvVar: tt.envVar}
			got, err := src.Token(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatal("Token() error = nil, want an error for an unset variable")
				}
				return
			}
			if err != nil {
				t.Fatalf("Token() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Token() = %v, want %v", got, tt.want)
			}
		})
	}
}
