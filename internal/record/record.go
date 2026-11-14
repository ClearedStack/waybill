// Package record defines the approved record Waybill takes as input and the
// deterministic checks that do not depend on the tool type.
package record

import (
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/strictjson"
)

const (
	// ContractVersion is the Muster contract this build understands.
	ContractVersion = "v1"

	// Inbound states Waybill holds in Muster's contract v1 routing table.
	StateApproved = "approved"
	StateBuilding = "building"

	// Supported owner determinations routed through Countersign.
	DecisionBuild      = "build"
	DecisionOptOut     = "opt_out"
	DecisionCorrection = "correction"

	// SLA windows defined in SPEC.md.
	IdentityVerificationSLA = 48 * time.Hour
	OptOutResolutionSLA     = 5 * 24 * time.Hour

	// MaxBytes is the largest record accepted.
	MaxBytes = 1 << 20
)

// Record is one approved or rebuilding item.
type Record struct {
	ContractVersion string          `json:"contract_version"`
	ItemID          string          `json:"item_id"`
	State           string          `json:"state"`
	Determination   Determination   `json:"determination"`
	Source          Source          `json:"source"`
	Slug            string          `json:"slug,omitempty"`
	Title           string          `json:"title"`
	Summary         string          `json:"summary,omitempty"`
	ScopeLimits     []string        `json:"scope_limits,omitempty"`
	Authority       []Authority     `json:"authority"`
	ToolType        string          `json:"tool_type"`
	Tool            json.RawMessage `json:"tool"`
	// VendorObservations is whatever earlier stages saved about the observed
	// product (snapshots, labels, links, scraped examples). It is accepted in
	// standalone records so that a record carrying it is not refused, and it is
	// never read: nothing in it can reach the build specification.
	VendorObservations json.RawMessage `json:"vendor_observations,omitempty"`
}

// Determination is the owner's decision recorded in Countersign, including
// optional 48h identity-verification and 5d opt-out / correction timestamps.
type Determination struct {
	Decision           string `json:"decision"`
	OptOut             bool   `json:"opt_out,omitempty"`
	NeutralDescriptor  string `json:"neutral_descriptor,omitempty"`
	RequestedAt        string `json:"requested_at,omitempty"`
	IdentityVerifiedAt string `json:"identity_verified_at,omitempty"`
	EvaluatedAt        string `json:"evaluated_at,omitempty"`
}

// Source names the product the idea was seen in.
type Source struct {
	Product string `json:"product"`
	Vendor  string `json:"vendor"`
}

// Authority is one official government publication the tool is built from.
type Authority struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Citation string `json:"citation"`
	Title    string `json:"title"`
	URL      string `json:"url,omitempty"`
}

// SLA holds computed 48-hour identity verification and 5-day resolution status
// when SLA timestamps are supplied on Determination.
type SLA struct {
	RequestedAt           string `json:"requested_at"`
	IdentityVerifiedAt    string `json:"identity_verified_at"`
	VerificationDueAt     string `json:"verification_due_at"`
	VerificationWithinSLA bool   `json:"verification_within_sla"`
	ResolutionDueAt       string `json:"resolution_due_at"`
	EvaluatedAt           string `json:"evaluated_at,omitempty"`
	ResolutionWithinSLA   *bool  `json:"resolution_within_sla,omitempty"`
	OptOutActive          bool   `json:"opt_out_active"`
	RepublishRequired     bool   `json:"republish_required"`
}

// Sources are the official publications SPEC.md names.
var Sources = []string{"FAR", "DFARS", "NIST", "eCFR"}

var (
	musterItemIDRE = regexp.MustCompile(`^PA-(?:[0-9]{4}|[1-9][0-9]{4,8})$`)
	fixtureSlugRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	slugRE         = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	// NameRE is the shape of every identifier Waybill writes into a build
	// specification: authority ids, input names and output names.
	NameRE     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	toolTypeRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	dnsLabelRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// IsMusterItemID reports whether id has Logbook and Muster's PA-0001..PA-999999999 form.
func IsMusterItemID(id string) bool {
	return musterItemIDRE.MatchString(id)
}

// ValidItemID reports whether id is a Muster PA-0001 identifier or a standalone
// fixture identifier.
func ValidItemID(id string) bool {
	return IsMusterItemID(id) || (fixtureSlugRE.MatchString(id) && !strings.HasSuffix(id, "-") && !strings.Contains(id, "--"))
}

// ValidSlug reports whether s is a valid catalog/tool slug (at most 64 bytes).
func ValidSlug(s string) bool {
	return len(s) >= 1 && len(s) <= 64 && slugRE.MatchString(s)
}

// Parse decodes and checks the parts of a record that do not depend on the
// tool type. The returned record is non-nil whenever the item id could be
// read and is well formed, so a failure can still be tied to its item.
func Parse(data []byte) (*Record, *fault.Error) {
	if len(data) > MaxBytes {
		return nil, fault.Broken("", "the record is larger than the limit")
	}
	var rec Record
	if err := strictjson.Decode(data, &rec); err != nil {
		return nil, fault.Broken("", "the record is not valid JSON of the expected shape")
	}
	if !ValidItemID(rec.ItemID) {
		return nil, fault.Broken("item_id", "the item id is missing or malformed")
	}
	return &rec, rec.check()
}

// OptOutActive reports whether nominative attribution opt-out is active on r.
func (r *Record) OptOutActive() bool {
	return r.Determination.OptOut || r.Determination.Decision == DecisionOptOut
}

// NeutralPhrase returns the neutral descriptor used in place of Product and
// Vendor when an opt-out is active.
func (r *Record) NeutralPhrase() string {
	if r.Determination.NeutralDescriptor != "" {
		return r.Determination.NeutralDescriptor
	}
	return "an observed commercial government-contracting " + r.ToolType
}

// ComputeSLA evaluates the 48-hour identity verification and 5-day resolution
// SLAs when timestamps are present on Determination. It uses only the input
// timestamps and never reads the system clock.
func (r *Record) ComputeSLA() (*SLA, *fault.Error) {
	d := r.Determination
	needsSLA := d.Decision == DecisionOptOut || d.Decision == DecisionCorrection ||
		d.RequestedAt != "" || d.IdentityVerifiedAt != "" || d.EvaluatedAt != ""
	if !needsSLA {
		return nil, nil
	}
	reqTime, ok := parseRFC3339(d.RequestedAt)
	if !ok {
		return nil, fault.Broken("determination.requested_at", "the request timestamp is missing or not RFC3339 UTC")
	}
	verTime, ok := parseRFC3339(d.IdentityVerifiedAt)
	if !ok {
		return nil, fault.Broken("determination.identity_verified_at", "identity verification timestamp is missing or not RFC3339 UTC")
	}
	if verTime.Before(reqTime) {
		return nil, fault.Broken("determination.identity_verified_at", "identity verification precedes the request timestamp")
	}

	verDue := reqTime.Add(IdentityVerificationSLA)
	resDue := reqTime.Add(OptOutResolutionSLA)
	sla := &SLA{
		RequestedAt:           reqTime.UTC().Format(time.RFC3339),
		IdentityVerifiedAt:    verTime.UTC().Format(time.RFC3339),
		VerificationDueAt:     verDue.UTC().Format(time.RFC3339),
		VerificationWithinSLA: !verTime.After(verDue),
		ResolutionDueAt:       resDue.UTC().Format(time.RFC3339),
		OptOutActive:          r.OptOutActive(),
		RepublishRequired:     d.Decision == DecisionOptOut || d.Decision == DecisionCorrection || r.State == StateBuilding,
	}
	if d.EvaluatedAt != "" {
		evalTime, ok := parseRFC3339(d.EvaluatedAt)
		if !ok {
			return nil, fault.Broken("determination.evaluated_at", "the evaluation timestamp is not RFC3339 UTC")
		}
		if evalTime.Before(verTime) {
			return nil, fault.Broken("determination.evaluated_at", "the evaluation timestamp precedes identity verification")
		}
		within := !evalTime.After(resDue)
		sla.EvaluatedAt = evalTime.UTC().Format(time.RFC3339)
		sla.ResolutionWithinSLA = &within
	}
	return sla, nil
}

// EffectiveSlug returns r.Slug when given, or a deterministic hyphenated slug
// derived from r.Title (falling back to lower-cased ItemID).
func (r *Record) EffectiveSlug() string {
	if r.Slug != "" {
		return r.Slug
	}
	slug := slugify(r.Title, 64)
	if ValidSlug(slug) {
		return slug
	}
	return slugify(strings.ToLower(r.ItemID), 64)
}

// EffectiveSummary returns r.Summary when given, or a deterministic one-line
// summary derived from the tool type, title, and official citations.
func (r *Record) EffectiveSummary() string {
	if r.Summary != "" {
		return r.Summary
	}
	var cits []string
	for _, a := range r.Authority {
		cits = append(cits, a.Citation)
	}
	s := "Deterministic " + r.ToolType + " for " + r.Title + " (" + strings.Join(cits, "; ") + ")."
	if utf8.RuneCountInString(s) > 400 {
		runes := []rune(s)
		s = strings.TrimSpace(string(runes[:399])) + "."
	}
	return s
}

// EffectiveScopeLimits returns r.ScopeLimits when given, or a deterministic
// scope boundary statement anchored to the cited government publications.
func (r *Record) EffectiveScopeLimits() []string {
	if len(r.ScopeLimits) > 0 {
		out := make([]string, len(r.ScopeLimits))
		copy(out, r.ScopeLimits)
		return out
	}
	var cits []string
	for _, a := range r.Authority {
		cits = append(cits, a.Citation)
	}
	line := "Implements only the cited public rule (" + strings.Join(cits, "; ") + "); does not constitute legal or contracting advice."
	if utf8.RuneCountInString(line) > 240 {
		line = "Implements only the cited public government publications; does not constitute legal or contracting advice."
	}
	return []string{line}
}

// AuthorityLinks returns the deduplicated list of official https URLs across
// r.Authority in declaration order.
func (r *Record) AuthorityLinks() []string {
	seen := map[string]bool{}
	var links []string
	for _, a := range r.Authority {
		if a.URL != "" && !seen[a.URL] {
			seen[a.URL] = true
			links = append(links, a.URL)
		}
	}
	return links
}

func (r *Record) check() *fault.Error {
	if r.ContractVersion == "" {
		return fault.Broken("contract_version", "the contract version is missing")
	}
	if r.ContractVersion != ContractVersion {
		return fault.Unfamiliar("contract_version", "no rule covers this contract version")
	}
	if r.State != StateApproved && r.State != StateBuilding {
		return fault.Broken("state", "the item is not in the approved state")
	}
	if r.Determination.Decision == "" {
		return fault.Broken("determination.decision", "the determination is missing")
	}
	switch r.Determination.Decision {
	case DecisionBuild, DecisionOptOut, DecisionCorrection:
	default:
		return fault.Unfamiliar("determination.decision", "no rule covers this kind of determination")
	}
	if !Text(r.Source.Product, 80) || len(Fold(r.Source.Product)) < 3 {
		return fault.Broken("source.product", "the product name is missing or malformed")
	}
	if !Text(r.Source.Vendor, 80) || len(Fold(r.Source.Vendor)) < 3 {
		return fault.Broken("source.vendor", "the vendor name is missing or malformed")
	}
	foldedProduct := Fold(r.Source.Product)
	foldedVendor := Fold(r.Source.Vendor)

	if r.Determination.NeutralDescriptor != "" {
		if !Text(r.Determination.NeutralDescriptor, 80) {
			return fault.Broken("determination.neutral_descriptor", "the neutral descriptor is malformed")
		}
		fn := Fold(r.Determination.NeutralDescriptor)
		if strings.Contains(fn, foldedProduct) || strings.Contains(fn, foldedVendor) {
			return fault.Broken("determination.neutral_descriptor", "the neutral descriptor still contains the product or vendor name")
		}
	}
	if _, err := r.ComputeSLA(); err != nil {
		return err
	}
	if r.Slug != "" && !ValidSlug(r.Slug) {
		return fault.Broken("slug", "the slug is malformed")
	}
	if !Text(r.Title, 80) {
		return fault.Broken("title", "the title is missing or malformed")
	}
	if r.Summary != "" && !TextRunes(r.Summary, 400) {
		return fault.Broken("summary", "the summary is malformed")
	}
	if len(r.ScopeLimits) > 20 {
		return fault.Broken("scope_limits", "too many scope limits are given")
	}
	for i, lim := range r.ScopeLimits {
		if !TextRunes(lim, 240) {
			return fault.Broken("scope_limits["+itoa(i)+"]", "the scope limit is empty or malformed")
		}
	}
	if len(r.Authority) == 0 {
		return fault.Broken("authority", "no statutory authority is given")
	}
	if len(r.Authority) > 16 {
		return fault.Broken("authority", "too many authorities are given")
	}
	seen := map[string]bool{}
	for i, a := range r.Authority {
		at := "authority[" + itoa(i) + "]"
		if !NameRE.MatchString(a.ID) || seen[a.ID] {
			return fault.Broken(at+".id", "the authority id is missing, malformed or repeated")
		}
		seen[a.ID] = true
		if !isSource(a.Source) {
			return fault.Broken(at+".source", "the authority is not one of the official publications")
		}
		if !Text(a.Citation, 80) {
			return fault.Broken(at+".citation", "the citation is missing or malformed")
		}
		if !Text(a.Title, 120) {
			return fault.Broken(at+".title", "the authority title is missing or malformed")
		}
		if a.URL != "" {
			if !PublicHTTPSURL(a.URL) {
				return fault.Broken(at+".url", "the authority url is not a valid public https address")
			}
			fu := Fold(a.URL)
			if strings.Contains(fu, foldedProduct) || strings.Contains(fu, foldedVendor) {
				return fault.Broken(at+".url", "the authority url names the observed product or vendor")
			}
		}
	}
	if !toolTypeRE.MatchString(r.ToolType) {
		return fault.Broken("tool_type", "the tool type is missing or malformed")
	}
	return nil
}

func isSource(s string) bool {
	for _, known := range Sources {
		if s == known {
			return true
		}
	}
	return false
}

func parseRFC3339(s string) (time.Time, bool) {
	if s == "" || !strings.HasSuffix(s, "Z") {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// PublicHTTPSURL reports whether raw is an absolute https URL on a public
// dotted lower-case DNS name with no credentials, query credentials, fragments
// or private/cluster host suffixes.
func PublicHTTPSURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 512 || !utf8.ValidString(raw) {
		return false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '<' || r == '>' || r == '"' || r == '\'' {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	if host != u.Host && u.Port() == "" {
		return false
	}
	if host != strings.ToLower(host) || net.ParseIP(host) != nil {
		return false
	}
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !dnsLabelRE.MatchString(p) {
			return false
		}
	}
	tld := parts[len(parts)-1]
	switch tld {
	case "local", "internal", "localhost", "svc", "invalid", "lan":
		return false
	}
	return true
}

// Text reports whether s is acceptable free text: one line of valid UTF-8,
// not padded, at most max bytes, with no control characters, no markup and
// nothing that looks like a link.
func Text(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	return cleanLine(s)
}

// TextRunes reports whether s is acceptable one-line free text of at most
// maxRunes characters.
func TextRunes(s string, maxRunes int) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRunes || strings.TrimSpace(s) != s {
		return false
	}
	return cleanLine(s)
}

func cleanLine(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co, unicode.Cs) ||
			r == '<' || r == '>' || r == unicode.ReplacementChar {
			return false
		}
	}
	lower := strings.ToLower(s)
	return !strings.Contains(lower, "://") && !strings.Contains(lower, "www.")
}

// Fold lower-cases s and keeps only its letters and digits, so that names can
// be compared whatever their spacing and punctuation.
func Fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func slugify(s string, maxBytes int) string {
	var b strings.Builder
	prevHyphen := true
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if b.Len() >= maxBytes {
				break
			}
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len()+1 < maxBytes {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
