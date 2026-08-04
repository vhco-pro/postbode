// Command spike is the PRD-P0 live spike (plan Phase 3). It proves both
// live integrations — ClearFacts and Gmail — end-to-end before any
// architecture is committed to, and settles OQ-8 (the companyStatistics
// `type` argument) empirically.
//
// // DELETE AFTER P1 (F-07) — see Makefile's `spike` target note and plan
// Phase 15 task "Delete cmd/spike and its Makefile target". This file is a
// thin main over internal/clearfacts (Phase 2, fully unit-tested) and
// internal/gmailwatch (auth.go, labels.go — production code the daemon
// reuses from Phase 7 onward). Deleting this directory must remove zero
// production logic.
//
// This command TOUCHES PRODUCTION: round-trip (b) performs one real upload
// into a real accountant's ClearFacts queue. That is explicitly authorized
// (spec A-5) — see F-04/NF-11: every live upload is announced with its
// uuid, filename and destination administration, and "it appeared in the
// portal" is always a human confirmation (AC-5), never an assertion this
// program makes about itself.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vhco-pro/postbode/internal/clearfacts"
	"github.com/vhco-pro/postbode/internal/gmailwatch"
)

// testUploadFilename is F-02's mandated exact filename — never anything
// else, so a spot-check in the portal is unambiguous.
const testUploadFilename = "TEST-postbode-ignore.pdf"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is separated from main so it can be exercised without exiting the
// process; in practice it is not unit-tested end-to-end (it performs live
// network calls by design), but every decision function it calls into
// (selectAdministration, writeCompanyNumberConfig, shouldSkipGmailLeg,
// shouldSkipVerificationLegs, probeCompanyStatisticsTypes,
// gmailwatch.ResolveLabel, ...) is.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spike", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		skipAdmin       bool
		skipUpload      bool
		skipVerify      bool
		skipProvenance  bool
		skipStats       bool
		skipGmail       bool
		companyNumber   string
		uploadUUID      string
		configPath      string
		credentialsPath string
		tokenCachePath  string
	)
	fs.BoolVar(&skipAdmin, "skip-admin", false, "skip round-trip (a): the administrations query")
	fs.BoolVar(&skipUpload, "skip-upload", false, "skip round-trip (b): the live TEST-postbode-ignore.pdf upload (use -uuid to still run (c)/(c2) against an earlier upload)")
	fs.BoolVar(&skipVerify, "skip-verify", false, "skip round-trip (c): document(id:) verification")
	fs.BoolVar(&skipProvenance, "skip-provenance", false, "skip the F-06/F-56 provenance readback check (c2)")
	fs.BoolVar(&skipStats, "skip-stats", false, "skip round-trip (e): the companyStatistics type probe")
	fs.BoolVar(&skipGmail, "skip-gmail", false, "skip round-trip (d): Gmail OAuth + label resolve")
	fs.StringVar(&companyNumber, "company-number", "", "explicit companyNumber to use — REQUIRED if administrations() returns more than one (F-01, AC-1); never guessed")
	fs.StringVar(&uploadUUID, "uuid", "", "an existing uuid to verify/re-check provenance against when -skip-upload is set")

	defaultCfgPath, cfgErr := defaultConfigPath()
	fs.StringVar(&configPath, "config", defaultCfgPath, "path to write administration.company_number into (F-01)")
	fs.StringVar(&credentialsPath, "credentials", "credentials.json", "path to the Google OAuth desktop-app credentials file (D-2)")
	fs.StringVar(&tokenCachePath, "token-cache", defaultTokenCachePath(), "path to cache the Gmail OAuth token")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if cfgErr != nil && configPath == "" {
		_, _ = fmt.Fprintf(stderr, "spike: cannot determine default config path: %v (pass -config explicitly)\n", cfgErr)
		return 2
	}

	ctx := context.Background()
	var results []legResult

	client := clearfacts.NewClient(clearfacts.EnvTokenSource{})

	// --- (a) administrations ---------------------------------------
	chosenCompanyNumber := companyNumber
	if skipAdmin {
		results = append(results, legResult{Name: "(a) administrations", Status: legSkipped, Detail: "skipped via -skip-admin"})
	} else {
		admins, err := client.Administrations(ctx, 0, 0)
		if err != nil {
			results = append(results, legResult{Name: "(a) administrations", Status: legFailed, Detail: err.Error()})
		} else {
			_, _ = fmt.Fprintln(stdout, "administrations:")
			for _, a := range admins {
				_, _ = fmt.Fprintf(stdout, "  companyNumber=%s\n", a.CompanyNumber)
			}
			cn, err := selectAdministration(admins, companyNumber)
			if err != nil {
				results = append(results, legResult{Name: "(a) administrations", Status: legFailed, Detail: err.Error()})
			} else if err := writeCompanyNumberConfig(configPath, cn); err != nil {
				results = append(results, legResult{Name: "(a) administrations", Status: legFailed, Detail: fmt.Sprintf("write config: %v", err)})
			} else {
				_, _ = fmt.Fprintf(stdout, "wrote administration.company_number=%s to %s\n", cn, configPath)
				chosenCompanyNumber = cn
				results = append(results, legResult{Name: "(a) administrations", Status: legPassed})
			}
		}
	}

	// --- (b) upload TEST-postbode-ignore.pdf ------------------------
	var uploadedFile *clearfacts.File
	switch {
	case skipUpload:
		results = append(results, legResult{Name: "(b) upload " + testUploadFilename, Status: legSkipped, Detail: "skipped via -skip-upload"})
	case chosenCompanyNumber == "":
		results = append(results, legResult{Name: "(b) upload " + testUploadFilename, Status: legFailed, Detail: "no companyNumber available — leg (a) failed/was skipped without -company-number"})
	default:
		file, err := client.UploadFile(ctx, clearfacts.UploadInput{
			CompanyNumber:  chosenCompanyNumber,
			Filename:       testUploadFilename,
			ItemID:         "spike",
			GmailMessageID: "spike",
			SHA256:         "spike-no-source-message",
			File:           bytes.NewReader(generateTestPDF()),
		})
		if err != nil {
			results = append(results, legResult{Name: "(b) upload " + testUploadFilename, Status: legFailed, Detail: err.Error()})
		} else {
			uploadedFile = file
			uploadUUID = file.UUID
			// F-04/NF-11: a single announcement line naming uuid, filename
			// and destination administration — the developer's spot-check
			// anchor. This is the ONLY thing this program asserts about the
			// portal; whether it actually appeared there is confirmed by a
			// human (AC-5), never by this program.
			_, _ = fmt.Fprintf(stdout, "LIVE UPLOAD: uuid=%s filename=%s administration=%s amountOfPages=%d\n",
				file.UUID, file.Name, chosenCompanyNumber, file.AmountOfPages)
			results = append(results, legResult{Name: "(b) upload " + testUploadFilename, Status: legPassed})
		}
	}

	// --- (c) document(id:) verify + (c2) provenance readback --------
	skipVerifyLegs, verifySkipReason := shouldSkipVerificationLegs(skipVerify, skipUpload, uploadUUID)
	if skipVerifyLegs {
		results = append(results, legResult{Name: "(c) document(id:) verify", Status: legSkipped, Detail: verifySkipReason})
		results = append(results, legResult{Name: "(c2) provenance readback", Status: legSkipped, Detail: verifySkipReason})
	} else {
		doc, err := client.Document(ctx, uploadUUID)
		switch {
		case err != nil:
			results = append(results, legResult{Name: "(c) document(id:) verify", Status: legFailed, Detail: err.Error()})
			results = append(results, legResult{Name: "(c2) provenance readback", Status: legSkipped, Detail: "leg (c) failed"})
		case doc == nil:
			results = append(results, legResult{Name: "(c) document(id:) verify", Status: legFailed, Detail: "document(id:) resolved with a nil document"})
			results = append(results, legResult{Name: "(c2) provenance readback", Status: legSkipped, Detail: "leg (c) failed"})
		default:
			_, _ = fmt.Fprintln(stdout, "verified: true")
			results = append(results, legResult{Name: "(c) document(id:) verify", Status: legPassed})

			if skipProvenance {
				results = append(results, legResult{Name: "(c2) provenance readback", Status: legSkipped, Detail: "skipped via -skip-provenance"})
			} else {
				_, _ = fmt.Fprintf(stdout, "File payload: uuid=%s name=%s amountOfPages=%d comment=%q tags=%v\n",
					doc.File.UUID, doc.File.Name, doc.File.AmountOfPages, doc.File.Comment, doc.File.Tags)

				switch {
				case uploadedFile == nil:
					results = append(results, legResult{Name: "(c2) provenance readback", Status: legPassed, Detail: "no fresh upload in this run to diff against; payload printed above for manual inspection"})
				case doc.File.Comment != uploadedFile.Comment:
					results = append(results, legResult{Name: "(c2) provenance readback", Status: legFailed, Detail: fmt.Sprintf("comment changed on readback: uploaded=%q got=%q", uploadedFile.Comment, doc.File.Comment)})
				case !slices.Equal(doc.File.Tags, uploadedFile.Tags):
					results = append(results, legResult{Name: "(c2) provenance readback", Status: legFailed, Detail: fmt.Sprintf("tags changed on readback: uploaded=%v got=%v", uploadedFile.Tags, doc.File.Tags)})
				default:
					results = append(results, legResult{Name: "(c2) provenance readback", Status: legPassed})
				}
			}
		}
	}

	// --- (e) companyStatistics type probe ----------------------------
	if skipStats {
		results = append(results, legResult{Name: "(e) companyStatistics type probe", Status: legSkipped, Detail: "skipped via -skip-stats"})
	} else {
		now := time.Now().UTC()
		startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		call := func(ctx context.Context, typ string) ([]clearfacts.CompanyStatistic, error) {
			return client.CompanyStatistics(ctx, clearfacts.CompanyStatisticsInput{
				Type:          typ,
				StartPeriod:   startOfMonth.Format("2006-01-02"),
				EndPeriod:     now.Format("2006-01-02"),
				CompanyNumber: chosenCompanyNumber,
				InvoiceType:   "PURCHASE",
			})
		}
		probe := probeCompanyStatisticsTypes(ctx, call, statisticsTypeCandidates)
		_, _ = fmt.Fprint(stdout, formatStatisticsProbeResult(probe))
		// AC-5b: either outcome — a working type, or an explicit
		// "no working type found" — is a pass. Only a program error would
		// fail this leg, and probeCompanyStatisticsTypes has none.
		results = append(results, legResult{Name: "(e) companyStatistics type probe", Status: legPassed})
	}

	// --- (d) Gmail: OAuth + 5 newest messages + label resolve --------
	skipGmailLeg, gmailSkipReason := shouldSkipGmailLeg(skipGmail, gmailwatch.CredentialsFileExists(credentialsPath))
	if skipGmailLeg {
		results = append(results, legResult{Name: "(d) gmail auth + label resolve", Status: legSkipped, Detail: gmailSkipReason})
	} else {
		results = append(results, runGmailLeg(ctx, credentialsPath, tokenCachePath, stdout))
	}

	_, _ = fmt.Fprintln(stdout)
	_, _ = fmt.Fprint(stdout, printSummary(results))
	_, _ = fmt.Fprintln(stdout)
	if chosenCompanyNumber != "" && !skipUpload {
		_, _ = fmt.Fprintf(stdout, "Next: open the ClearFacts portal, confirm exactly one %s appeared in \"In verwerking\" for administration %s, and delete it (AC-5, D-4). This program never asserts that on its own.\n",
			testUploadFilename, chosenCompanyNumber)
	}

	if anyFailed(results) {
		return 1
	}
	return 0
}

// defaultTokenCachePath returns the Gmail OAuth token cache location under
// the same Application Support directory the daemon will use for its spool
// (spec §6.4/§8). Gitignored via the *.token / token.json patterns (F-63).
func defaultTokenCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "token.json"
	}
	return filepath.Join(home, "Library", "Application Support", "Postbode", "token.json")
}
