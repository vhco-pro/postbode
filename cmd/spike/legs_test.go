package main

import "testing"

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "when `credentials.json` is missing the spike must **skip leg (d)** with a clear SKIPPED message and still exit 0 if the ClearFacts legs passed — it must not crash or block the other legs"
func TestShouldSkipGmailLegMissingCredentials(t *testing.T) {
	skip, reason := shouldSkipGmailLeg(false, false)
	if !skip {
		t.Fatal("shouldSkipGmailLeg: want skip=true when credentials.json is missing")
	}
	if reason == "" {
		t.Error("shouldSkipGmailLeg: want a non-empty SKIPPED reason")
	}
}

func TestShouldSkipGmailLegCredentialsPresent(t *testing.T) {
	skip, _ := shouldSkipGmailLeg(false, true)
	if skip {
		t.Error("shouldSkipGmailLeg: want skip=false when credentials.json exists and no explicit skip flag was given")
	}
}

func TestShouldSkipGmailLegExplicitFlag(t *testing.T) {
	skip, reason := shouldSkipGmailLeg(true, true)
	if !skip {
		t.Error("shouldSkipGmailLeg: want skip=true when -skip-gmail is set, even with credentials present")
	}
	if reason == "" {
		t.Error("shouldSkipGmailLeg: want a non-empty reason")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "Flags to run legs selectively (e.g. `-skip-upload`) so the developer can re-run read-only legs without a second real upload"
func TestShouldSkipVerificationLegsWithoutUploadOrUUID(t *testing.T) {
	skip, reason := shouldSkipVerificationLegs(false, true, "")
	if !skip {
		t.Fatal("shouldSkipVerificationLegs: want skip=true when upload was skipped and no -uuid was given")
	}
	if reason == "" {
		t.Error("shouldSkipVerificationLegs: want a non-empty reason")
	}
}

func TestShouldSkipVerificationLegsWithUUIDButNoUpload(t *testing.T) {
	skip, _ := shouldSkipVerificationLegs(false, true, "fake-uuid-0001")
	if skip {
		t.Error("shouldSkipVerificationLegs: want skip=false when -uuid is supplied even though upload was skipped — that's the whole point of re-running read-only legs")
	}
}

func TestShouldSkipVerificationLegsRunsAfterFreshUpload(t *testing.T) {
	skip, _ := shouldSkipVerificationLegs(false, false, "")
	if skip {
		t.Error("shouldSkipVerificationLegs: want skip=false right after a fresh upload produced a uuid")
	}
}

func TestAnyFailedIgnoresSkipped(t *testing.T) {
	results := []legResult{
		{Name: "a", Status: legPassed},
		{Name: "d", Status: legSkipped, Detail: "no credentials"},
	}
	if anyFailed(results) {
		t.Error("anyFailed: SKIPPED legs must not count as failures")
	}
}

func TestAnyFailedDetectsFailure(t *testing.T) {
	results := []legResult{
		{Name: "a", Status: legPassed},
		{Name: "b", Status: legFailed, Detail: "boom"},
	}
	if !anyFailed(results) {
		t.Error("anyFailed: a FAIL leg must be detected")
	}
}
