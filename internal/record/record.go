// Package record defines the approved record Waybill takes as input.
//
// SPEC.md does not give this shape. It is this repository's working
// assumption of what Muster hands over for an item in the "approved" state,
// and it is listed as an open question in the README.
package record

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/clearedstack/waybill/internal/fault"
	"github.com/clearedstack/waybill/internal/strictjson"
)

const (
	// ContractVersion is the Muster contract this build understands.
	ContractVersion = "v1"
	// StateApproved is the only inbound state.
	StateApproved = "approved"
	// DecisionBuild is the only determination this milestone handles.
	DecisionBuild = "build"
	// MaxBytes is the largest record accepted.
	MaxBytes = 1 << 20
)

// Record is one approved item.
type Record struct {
	ContractVersion string          `json:"contract_version"`
	ItemID          string          `json:"item_id"`
	State           string          `json:"state"`
	Determination   Determination   `json:"determination"`
	Source          Source          `json:"source"`
	Title           string          `json:"title"`
	Authority       []Authority     `json:"authority"`
	ToolType        string          `json:"tool_type"`
	Tool            json.RawMessage `json:"tool"`
	// VendorObservations is whatever earlier stages saved about the observed
	// product (snapshots, labels, links, scraped examples). It is accepted so
	// that a record carrying it is not refused, and it is never read: nothing
	// in it can reach the build specification.
	VendorObservations json.RawMessage `json:"vendor_observations,omitempty"`
}

// Determination is the owner's decision recorded in Countersign.
type Determination struct {
	Decision string `json:"decision"`
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
}

// Sources are the official publications SPEC.md names.
var Sources = []string{"FAR", "DFARS", "NIST", "eCFR"}

var (
	itemIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// NameRE is the shape of every identifier Waybill writes into a build
	// specification: authority ids, input names and output names.
	NameRE     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	toolTypeRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

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
	if !itemIDRE.MatchString(rec.ItemID) {
		return nil, fault.Broken("item_id", "the item id is missing or malformed")
	}
	return &rec, rec.check()
}

func (r *Record) check() *fault.Error {
	if r.ContractVersion == "" {
		return fault.Broken("contract_version", "the contract version is missing")
	}
	if r.ContractVersion != ContractVersion {
		return fault.Unfamiliar("contract_version", "no rule covers this contract version")
	}
	if r.State != StateApproved {
		return fault.Broken("state", "the item is not in the approved state")
	}
	if r.Determination.Decision == "" {
		return fault.Broken("determination.decision", "the determination is missing")
	}
	if r.Determination.Decision != DecisionBuild {
		return fault.Unfamiliar("determination.decision", "no rule covers this kind of determination")
	}
	if !Text(r.Source.Product, 80) || len(Fold(r.Source.Product)) < 3 {
		return fault.Broken("source.product", "the product name is missing or malformed")
	}
	if !Text(r.Source.Vendor, 80) || len(Fold(r.Source.Vendor)) < 3 {
		return fault.Broken("source.vendor", "the vendor name is missing or malformed")
	}
	if !Text(r.Title, 80) {
		return fault.Broken("title", "the title is missing or malformed")
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

// Text reports whether s is acceptable free text: one line of valid UTF-8,
// not padded, at most max bytes, with no control characters, no markup and
// nothing that looks like a link.
func Text(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '<' || r == '>' || r == unicode.ReplacementChar {
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

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
