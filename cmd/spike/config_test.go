package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "write that value into `~/.config/postbode/config.yaml` under `administration.company_number` (F-01, AC-1)"
func TestWriteCompanyNumberConfigCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.yaml")

	if err := writeCompanyNumberConfig(path, "BE0123456789"); err != nil {
		t.Fatalf("writeCompanyNumberConfig: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("config file mode = %v, want 0600", got)
	}

	var got struct {
		Administration struct {
			CompanyNumber string `yaml:"company_number"`
		} `yaml:"administration"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got.Administration.CompanyNumber != "BE0123456789" {
		t.Errorf("administration.company_number = %q, want %q", got.Administration.CompanyNumber, "BE0123456789")
	}
}

func TestWriteCompanyNumberConfigPreservesExistingKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := "gmail:\n  watch: inbox\nadministration:\n  company_number: BE0000000000\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := writeCompanyNumberConfig(path, "BE0123456789"); err != nil {
		t.Fatalf("writeCompanyNumberConfig: %v", err)
	}

	var got struct {
		Gmail struct {
			Watch string `yaml:"watch"`
		} `yaml:"gmail"`
		Administration struct {
			CompanyNumber string `yaml:"company_number"`
		} `yaml:"administration"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got.Gmail.Watch != "inbox" {
		t.Errorf("gmail.watch = %q, want it preserved as %q", got.Gmail.Watch, "inbox")
	}
	if got.Administration.CompanyNumber != "BE0123456789" {
		t.Errorf("administration.company_number = %q, want it updated to %q", got.Administration.CompanyNumber, "BE0123456789")
	}
}

func TestWriteCompanyNumberConfigRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := writeCompanyNumberConfig(path, ""); err == nil {
		t.Error("writeCompanyNumberConfig with an empty companyNumber: want an error, got nil")
	}
}
