// Package config loads and validates Postbode's ~/.config/postbode/config.yaml
// (spec §6.5, PRD §6.3). The shape is normative: PRD §6.3's sample plus the
// P1 additions (gmail.watch, gmail.query_window_days, gmail.poll_interval,
// gmail.submitted_label, vendors.known_peppol, retention_days,
// upload.max_concurrent, upload.min_interval, ui.port,
// administration.company_number).
//
// # Startup validation (F-29)
//
// The file may be partial — every field has a sensible default — but an
// unknown key or a malformed glob in a rule must fail loudly, naming the
// offending line number, and must never result in the daemon starting with a
// partially-applied ruleset (G-1: a silently dropped rule causes missed
// invoices). Load therefore does not lean on yaml.v3's KnownFields decoder
// (whose errors are decoder-shaped, not line-number-first); it walks the
// parsed *yaml.Node tree itself, checking every mapping key against an
// explicit allow-list at each level and compiling every glob eagerly, so
// every rejection carries the exact yaml.Node.Line of the offending key or
// pattern.
package config

import (
	"fmt"
	"os"
	"path"
	"time"

	"gopkg.in/yaml.v3"
)

// AttachmentKind is the value space for a rule's has/has_no matcher.
type AttachmentKind string

const (
	AttachmentPDF   AttachmentKind = "pdf"
	AttachmentImage AttachmentKind = "image"
)

// Matcher is one match/deny predicate object (F-26). A zero-value field
// means "not set" — that dimension is not considered when evaluating the
// rule.
type Matcher struct {
	From            string         // glob, e.g. "*@ovh.com"
	Subject         string         // case-insensitive substring
	Has             AttachmentKind // "pdf" | "image", empty = unset
	HasNo           AttachmentKind // "pdf" | "image", empty = unset
	ListUnsubscribe *bool          // nil = unset
}

// Rule is one first-match-wins rules entry (F-26). Exactly one of Allow or
// Deny is set — Load rejects a rule item with both or neither.
type Rule struct {
	Allow *Matcher
	Deny  *Matcher
	// Line is the source line of the rule's "match"/"deny" key, kept for
	// callers that want to surface it in logs; Load itself already fails on
	// any structural or glob problem before returning.
	Line int
}

// Administration holds the ClearFacts administration identifier (F-01, A-12:
// company_number supersedes the PRD's deprecated vatnumber key).
type Administration struct {
	CompanyNumber string
}

// Gmail holds the Gmail polling configuration (F-11, F-12, F-13, F-14).
type Gmail struct {
	// Watch is F-11's scope: "all" (default — the whole mailbox except sent,
	// drafts, trash and spam) or "inbox" (only messages carrying INBOX).
	// "inbox" was the default until v1.11; it misses mail that never carries
	// the INBOX label, such as anything Gmail's POP3 fetcher imports with
	// "Archive incoming messages" on. See internal/gmailwatch/watchscope.go.
	Watch           string
	QueryWindowDays int // F-13, default 30
	PollInterval    time.Duration
	SubmittedLabel  string // F-14, default "VH&Co/submitted"

	// FailureBudget is F-70/F-72: how many consecutive failures one message
	// is allowed to cost the whole poll before it is parked and the loop
	// moves on. Default 3, which at the default 5m PollInterval bounds a
	// single bad message to roughly 15 minutes of stalled mailbox.
	FailureBudget int
	// ParkRetryCooldown is F-75's base interval before a parked message is
	// automatically retried. Doubles per attempt, capped at 24h. Default 6h.
	ParkRetryCooldown time.Duration
	// ParkRetryAttempts is F-75's bound on automatic retries. After this
	// many, the message stays parked and stays reported but generates no
	// further automatic work — only `postbode retry` can schedule another.
	// Default 3.
	ParkRetryAttempts int
	// PollFailureBudget is F-81/F-82: how many consecutive poll cycles may
	// fail to make progress before the daemon says so out loud. Default 3.
	PollFailureBudget int
}

// Vendors holds vendor-level overrides (F-36).
type Vendors struct {
	KnownPeppol []string
}

// Upload holds uploader pacing configuration (F-54).
type Upload struct {
	MaxConcurrent int
	MinInterval   time.Duration
}

// UI holds the localhost review UI configuration (F-42).
type UI struct {
	Port int
}

// Config is the fully loaded, defaulted, validated configuration.
type Config struct {
	Administration Administration
	Gmail          Gmail
	Vendors        Vendors
	RetentionDays  int // F-24, default 30
	Upload         Upload
	UI             UI
	Rules          []Rule
	// Default is the raw text of the top-level `default:` key, e.g.
	// "queue_if(pdf_attachment || image_attachment || invoice_keywords)" in
	// the PRD §6.3 sample. It is accepted (so the exact PRD sample loads)
	// but is documentation only — the actual default heuristic (F-27) is
	// fixed behaviour implemented by internal/rules, not parsed from here.
	Default string
}

// Default returns the configuration used when no config file exists yet
// (first run, before cmd/spike has written one).
func Default() Config {
	return Config{
		Administration: Administration{CompanyNumber: ""},
		Gmail: Gmail{
			Watch:             "all",
			QueryWindowDays:   30,
			PollInterval:      5 * time.Minute,
			SubmittedLabel:    "VH&Co/submitted",
			FailureBudget:     3,
			ParkRetryCooldown: 6 * time.Hour,
			ParkRetryAttempts: 3,
			PollFailureBudget: 3,
		},
		Vendors:       Vendors{KnownPeppol: nil},
		RetentionDays: 30,
		Upload: Upload{
			MaxConcurrent: 1,
			MinInterval:   2 * time.Second,
		},
		UI:    UI{Port: 7391},
		Rules: nil,
	}
}

// Load reads and validates the config file at path. A missing file is not
// an error: it returns Default() unchanged, since the file may not exist
// until cmd/spike writes it (§6.3). Any structural problem — an unknown
// key at any level, a rule with neither/both of match and deny, or a
// malformed "from" glob — is returned as a *ValidationError naming the
// offending line, and the returned Config is the zero value: callers MUST
// treat a non-nil error as "refuse to start" (F-29) and must not fall back
// to partial defaults.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(data)
}

// ValidationError is returned by Load/Parse for any structural or content
// problem in the config file. Line is 1-based and always set when known.
type ValidationError struct {
	Line int
	Msg  string
}

func (e *ValidationError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("config: line %d: %s", e.Line, e.Msg)
	}
	return fmt.Sprintf("config: %s", e.Msg)
}

func errAt(line int, format string, args ...any) error {
	return &ValidationError{Line: line, Msg: fmt.Sprintf(format, args...)}
}

// Parse validates and decodes raw YAML bytes into a Config, applying
// Default() for every field the document does not set.
func Parse(data []byte) (Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Config{}, fmt.Errorf("config: parse yaml: %w", err)
	}
	cfg := Default()
	if len(root.Content) == 0 {
		// Empty document.
		return cfg, nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return Config{}, errAt(doc.Line, "top-level document must be a mapping")
	}

	allowedTop := map[string]bool{
		"administration": true,
		"gmail":          true,
		"vendors":        true,
		"retention_days": true,
		"upload":         true,
		"ui":             true,
		"rules":          true,
		"default":        true,
	}

	pairs, err := mappingPairs(doc, allowedTop)
	if err != nil {
		return Config{}, err
	}

	for key, val := range pairs {
		switch key {
		case "administration":
			if err := decodeAdministration(val, &cfg.Administration); err != nil {
				return Config{}, err
			}
		case "gmail":
			if err := decodeGmail(val, &cfg.Gmail); err != nil {
				return Config{}, err
			}
		case "vendors":
			if err := decodeVendors(val, &cfg.Vendors); err != nil {
				return Config{}, err
			}
		case "retention_days":
			n, err := decodeInt(val)
			if err != nil {
				return Config{}, err
			}
			cfg.RetentionDays = n
		case "upload":
			if err := decodeUpload(val, &cfg.Upload); err != nil {
				return Config{}, err
			}
		case "ui":
			if err := decodeUI(val, &cfg.UI); err != nil {
				return Config{}, err
			}
		case "default":
			s, err := decodeString(val)
			if err != nil {
				return Config{}, err
			}
			cfg.Default = s
		case "rules":
			rules, err := decodeRules(val)
			if err != nil {
				return Config{}, err
			}
			cfg.Rules = rules
		}
	}

	return cfg, nil
}

// mappingPairs walks a MappingNode's key/value pairs in document order,
// rejecting any key not present in allowed, with the offending key's line
// number.
func mappingPairs(node *yaml.Node, allowed map[string]bool) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, errAt(node.Line, "expected a mapping")
	}
	out := make(map[string]*yaml.Node, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		key := keyNode.Value
		if !allowed[key] {
			return nil, errAt(keyNode.Line, "unknown key %q", key)
		}
		out[key] = valNode
	}
	return out, nil
}

func decodeString(node *yaml.Node) (string, error) {
	if node.Kind != yaml.ScalarNode {
		return "", errAt(node.Line, "expected a string")
	}
	return node.Value, nil
}

func decodeInt(node *yaml.Node) (int, error) {
	if node.Kind != yaml.ScalarNode {
		return 0, errAt(node.Line, "expected an integer")
	}
	var n int
	if err := node.Decode(&n); err != nil {
		return 0, errAt(node.Line, "expected an integer: %v", err)
	}
	return n, nil
}

func decodeBool(node *yaml.Node) (bool, error) {
	if node.Kind != yaml.ScalarNode {
		return false, errAt(node.Line, "expected a boolean")
	}
	var b bool
	if err := node.Decode(&b); err != nil {
		return false, errAt(node.Line, "expected a boolean: %v", err)
	}
	return b, nil
}

func decodeDuration(node *yaml.Node) (time.Duration, error) {
	s, err := decodeString(node)
	if err != nil {
		return 0, err
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, errAt(node.Line, "invalid duration %q: %v", s, err)
	}
	return d, nil
}

func decodeStringSlice(node *yaml.Node) ([]string, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, errAt(node.Line, "expected a list of strings")
	}
	out := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		s, err := decodeString(item)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func decodeAdministration(node *yaml.Node, a *Administration) error {
	pairs, err := mappingPairs(node, map[string]bool{"company_number": true})
	if err != nil {
		return err
	}
	if v, ok := pairs["company_number"]; ok {
		s, err := decodeString(v)
		if err != nil {
			return err
		}
		a.CompanyNumber = s
	}
	return nil
}

func decodeGmail(node *yaml.Node, g *Gmail) error {
	pairs, err := mappingPairs(node, map[string]bool{
		"watch":               true,
		"query_window_days":   true,
		"poll_interval":       true,
		"submitted_label":     true,
		"failure_budget":      true,
		"park_retry_cooldown": true,
		"park_retry_attempts": true,
		"poll_failure_budget": true,
	})
	if err != nil {
		return err
	}
	if v, ok := pairs["watch"]; ok {
		s, err := decodeString(v)
		if err != nil {
			return err
		}
		g.Watch = s
	}
	if v, ok := pairs["query_window_days"]; ok {
		n, err := decodeInt(v)
		if err != nil {
			return err
		}
		g.QueryWindowDays = n
	}
	if v, ok := pairs["poll_interval"]; ok {
		d, err := decodeDuration(v)
		if err != nil {
			return err
		}
		g.PollInterval = d
	}
	if v, ok := pairs["submitted_label"]; ok {
		s, err := decodeString(v)
		if err != nil {
			return err
		}
		g.SubmittedLabel = s
	}

	// The four resilience bounds (F-87). All four are rejected at or below
	// zero, with the offending line number (F-29). This is not pedantry: a
	// `failure_budget: 0` would park every message on its first hiccup and
	// quietly turn a typo into a G-1 hazard, and a zero cooldown would spin
	// a broken message through the poll on every tick.
	if v, ok := pairs["failure_budget"]; ok {
		n, err := decodePositiveInt(v, "failure_budget")
		if err != nil {
			return err
		}
		g.FailureBudget = n
	}
	if v, ok := pairs["park_retry_cooldown"]; ok {
		d, err := decodeDuration(v)
		if err != nil {
			return err
		}
		if d <= 0 {
			return errAt(v.Line, "park_retry_cooldown must be greater than zero, got %s", d)
		}
		g.ParkRetryCooldown = d
	}
	if v, ok := pairs["park_retry_attempts"]; ok {
		n, err := decodePositiveInt(v, "park_retry_attempts")
		if err != nil {
			return err
		}
		g.ParkRetryAttempts = n
	}
	if v, ok := pairs["poll_failure_budget"]; ok {
		n, err := decodePositiveInt(v, "poll_failure_budget")
		if err != nil {
			return err
		}
		g.PollFailureBudget = n
	}
	return nil
}

// decodePositiveInt is decodeInt plus F-87's "> 0" rule, keeping the
// offending line number in the message the way every other config error
// does (F-29).
func decodePositiveInt(node *yaml.Node, key string) (int, error) {
	n, err := decodeInt(node)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, errAt(node.Line, "%s must be greater than zero, got %d", key, n)
	}
	return n, nil
}

func decodeVendors(node *yaml.Node, v *Vendors) error {
	pairs, err := mappingPairs(node, map[string]bool{"known_peppol": true})
	if err != nil {
		return err
	}
	if n, ok := pairs["known_peppol"]; ok {
		list, err := decodeStringSlice(n)
		if err != nil {
			return err
		}
		v.KnownPeppol = list
	}
	return nil
}

func decodeUpload(node *yaml.Node, u *Upload) error {
	pairs, err := mappingPairs(node, map[string]bool{
		"max_concurrent": true,
		"min_interval":   true,
	})
	if err != nil {
		return err
	}
	if v, ok := pairs["max_concurrent"]; ok {
		n, err := decodeInt(v)
		if err != nil {
			return err
		}
		u.MaxConcurrent = n
	}
	if v, ok := pairs["min_interval"]; ok {
		d, err := decodeDuration(v)
		if err != nil {
			return err
		}
		u.MinInterval = d
	}
	return nil
}

func decodeUI(node *yaml.Node, u *UI) error {
	pairs, err := mappingPairs(node, map[string]bool{"port": true})
	if err != nil {
		return err
	}
	if v, ok := pairs["port"]; ok {
		n, err := decodeInt(v)
		if err != nil {
			return err
		}
		u.Port = n
	}
	return nil
}

func decodeRules(node *yaml.Node) ([]Rule, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, errAt(node.Line, "rules must be a list")
	}
	rules := make([]Rule, 0, len(node.Content))
	for _, item := range node.Content {
		r, err := decodeRule(item)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

var allowedRuleKeys = map[string]bool{"match": true, "deny": true}

func decodeRule(node *yaml.Node) (Rule, error) {
	pairs, err := mappingPairs(node, allowedRuleKeys)
	if err != nil {
		return Rule{}, err
	}
	matchNode, hasMatch := pairs["match"]
	denyNode, hasDeny := pairs["deny"]
	if hasMatch == hasDeny {
		// Neither or both present.
		return Rule{}, errAt(node.Line, "rule must have exactly one of \"match\" (allow) or \"deny\"")
	}

	r := Rule{Line: node.Line}
	if hasMatch {
		m, err := decodeMatcher(matchNode)
		if err != nil {
			return Rule{}, err
		}
		r.Allow = m
	} else {
		m, err := decodeMatcher(denyNode)
		if err != nil {
			return Rule{}, err
		}
		r.Deny = m
	}
	return r, nil
}

var allowedMatcherKeys = map[string]bool{
	"from":             true,
	"subject":          true,
	"has":              true,
	"has_no":           true,
	"list_unsubscribe": true,
}

func decodeMatcher(node *yaml.Node) (*Matcher, error) {
	pairs, err := mappingPairs(node, allowedMatcherKeys)
	if err != nil {
		return nil, err
	}
	m := &Matcher{}
	if v, ok := pairs["from"]; ok {
		s, err := decodeString(v)
		if err != nil {
			return nil, err
		}
		if _, err := path.Match(s, ""); err != nil {
			return nil, errAt(v.Line, "invalid glob %q in \"from\": %v", s, err)
		}
		m.From = s
	}
	if v, ok := pairs["subject"]; ok {
		s, err := decodeString(v)
		if err != nil {
			return nil, err
		}
		m.Subject = s
	}
	if v, ok := pairs["has"]; ok {
		k, err := decodeAttachmentKind(v)
		if err != nil {
			return nil, err
		}
		m.Has = k
	}
	if v, ok := pairs["has_no"]; ok {
		k, err := decodeAttachmentKind(v)
		if err != nil {
			return nil, err
		}
		m.HasNo = k
	}
	if v, ok := pairs["list_unsubscribe"]; ok {
		b, err := decodeBool(v)
		if err != nil {
			return nil, err
		}
		m.ListUnsubscribe = &b
	}
	return m, nil
}

func decodeAttachmentKind(node *yaml.Node) (AttachmentKind, error) {
	s, err := decodeString(node)
	if err != nil {
		return "", err
	}
	switch AttachmentKind(s) {
	case AttachmentPDF, AttachmentImage:
		return AttachmentKind(s), nil
	default:
		return "", errAt(node.Line, "invalid attachment kind %q, must be \"pdf\" or \"image\"", s)
	}
}
