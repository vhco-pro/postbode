package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/config"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Config loader for `~/.config/postbode/config.yaml`, normative shape from PRD §6.3 plus the §6.5 P1 additions (`gmail.watch: inbox`, `vendors.known_peppol`, `retention_days`, `upload.max_concurrent`, `upload.min_interval`, `ui.port`, `administration.company_number`)"
func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"gmail.watch", cfg.Gmail.Watch, "inbox"},
		{"gmail.query_window_days", cfg.Gmail.QueryWindowDays, 30},
		{"gmail.poll_interval", cfg.Gmail.PollInterval, 5 * time.Minute},
		{"gmail.submitted_label", cfg.Gmail.SubmittedLabel, "VH&Co/submitted"},
		{"retention_days", cfg.RetentionDays, 30},
		{"upload.max_concurrent", cfg.Upload.MaxConcurrent, 1},
		{"upload.min_interval", cfg.Upload.MinInterval, 2 * time.Second},
		{"ui.port", cfg.UI.Port, 7391},
		{"administration.company_number", cfg.Administration.CompanyNumber, ""},
		{"vendors.known_peppol length", len(cfg.Vendors.KnownPeppol), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Config loader for `~/.config/postbode/config.yaml`, normative shape from PRD §6.3 plus the §6.5 P1 additions (`gmail.watch: inbox`, `vendors.known_peppol`, `retention_days`, `upload.max_concurrent`, `upload.min_interval`, `ui.port`, `administration.company_number`)"
func TestLoad_PartialFileKeepsRemainingDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("retention_days: 90\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.RetentionDays != 90 {
		t.Errorf("RetentionDays = %d, want 90", cfg.RetentionDays)
	}
	if cfg.Gmail.SubmittedLabel != "VH&Co/submitted" {
		t.Errorf("Gmail.SubmittedLabel = %q, want default to survive a partial file", cfg.Gmail.SubmittedLabel)
	}
	if cfg.UI.Port != 7391 {
		t.Errorf("UI.Port = %d, want default to survive a partial file", cfg.UI.Port)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Per-document, first-match-wins evaluation with `allow` (→ queue) and `deny` (→ never queue) forms, matching on `from` (glob), `subject` (substring, case-insensitive), `has`/`has_no` (`pdf`, `image`), `list_unsubscribe` (bool) (F-26, AC-8)"
func TestLoad_PRDSampleParsesExpectedRuleShape(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load("testdata/prd_sample.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Administration.CompanyNumber != "BE0123456789" {
		t.Errorf("Administration.CompanyNumber = %q, want BE0123456789", cfg.Administration.CompanyNumber)
	}
	if len(cfg.Rules) != 4 {
		t.Fatalf("len(Rules) = %d, want 4", len(cfg.Rules))
	}

	tests := []struct {
		name      string
		idx       int
		wantAllow bool
		wantDeny  bool
	}{
		{"rule 0 is allow (ovh)", 0, true, false},
		{"rule 1 is allow (github)", 1, true, false},
		{"rule 2 is deny (newsletter)", 2, false, true},
		{"rule 3 is deny (list_unsubscribe)", 3, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := cfg.Rules[tt.idx]
			if (r.Allow != nil) != tt.wantAllow {
				t.Errorf("rule %d Allow set = %v, want %v", tt.idx, r.Allow != nil, tt.wantAllow)
			}
			if (r.Deny != nil) != tt.wantDeny {
				t.Errorf("rule %d Deny set = %v, want %v", tt.idx, r.Deny != nil, tt.wantDeny)
			}
		})
	}

	if cfg.Rules[0].Allow.From != "*@ovh.com" || cfg.Rules[0].Allow.Has != config.AttachmentPDF {
		t.Errorf("rule 0 = %+v, want from=*@ovh.com has=pdf", cfg.Rules[0].Allow)
	}
	if cfg.Rules[2].Deny.From != "*@newsletter.*" {
		t.Errorf("rule 2 = %+v, want from=*@newsletter.*", cfg.Rules[2].Deny)
	}
	if cfg.Rules[3].Deny.ListUnsubscribe == nil || !*cfg.Rules[3].Deny.ListUnsubscribe || cfg.Rules[3].Deny.HasNo != config.AttachmentPDF {
		t.Errorf("rule 3 = %+v, want list_unsubscribe=true has_no=pdf", cfg.Rules[3].Deny)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Startup validation: unknown key or bad glob fails loudly with the offending line number and the daemon refuses to start; the previous run's queue is untouched (F-29)"
func TestLoad_ValidationFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		wantLine int
	}{
		{"unknown rule key names the offending line", "testdata/unknown_rule_key.yaml", 3},
		{"malformed glob names the offending line", "testdata/bad_glob.yaml", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Load(tt.path)
			if err == nil {
				t.Fatalf("Load(%s) error = nil, want an error", tt.path)
			}
			if !reflect.DeepEqual(cfg, config.Config{}) {
				t.Errorf("Load(%s) returned a non-zero Config alongside an error: %+v", tt.path, cfg)
			}
			var verr *config.ValidationError
			if !asValidationError(err, &verr) {
				t.Fatalf("Load(%s) error = %v (%T), want *config.ValidationError", tt.path, err, err)
			}
			if verr.Line != tt.wantLine {
				t.Errorf("Load(%s) line = %d, want %d", tt.path, verr.Line, tt.wantLine)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Startup validation: unknown key or bad glob fails loudly with the offending line number and the daemon refuses to start; the previous run's queue is untouched (F-29)"
func TestLoad_UnknownTopLevelKeyFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "retention_days: 30\nnope_not_a_real_key: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want an error for an unknown top-level key")
	}
	var verr *config.ValidationError
	if !asValidationError(err, &verr) {
		t.Fatalf("Load() error = %v (%T), want *config.ValidationError", err, err)
	}
	if verr.Line != 2 {
		t.Errorf("Load() line = %d, want 2", verr.Line)
	}
}

func asValidationError(err error, target **config.ValidationError) bool {
	verr, ok := err.(*config.ValidationError)
	if ok {
		*target = verr
	}
	return ok
}
