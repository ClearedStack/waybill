package buildspec

import "testing"

func TestEncodeDeterministicAndForbiddenKeys(t *testing.T) {
	sample := WaybillResult{
		Slug:  "sample-tool",
		Entry: CatalogEntry{Title: "Sample Tool", Summary: "Computes a sample rule."},
		Provenance: &CatalogProvenance{
			Note:  "Idea seen in: Sample\nWhat we took: idea.\nWhat we did differently: rule.\n",
			Links: []string{"https://rules.example/part-97"},
		},
		BuildSpec: &BuildSpec{
			Provenance: Provenance{ItemID: "PA-0001"},
			Authority:  []Authority{{ID: "r1", Source: "FAR", Citation: "FAR 97.1", Title: "Sample"}},
			Schema:     map[string]string{"b": "2", "a": "1"},
			Vectors:    []string{"v1"},
		},
		Spec: &MoldloftSpec{
			ContractVersion: "v1",
			ItemID:          "PA-0001",
			Tool:            MoldloftTool{Slug: "sample-tool", Title: "Sample Tool", Summary: "Computes a sample rule."},
			Pattern:         "calculator",
			Citations:       []MoldloftCitation{{ID: "r1", Label: "FAR 97.1"}},
			ScopeLimits:     []string{"Sample only."},
			Body:            map[string]string{"x": "1"},
			WorkedExamples:  []string{"e1"},
		},
	}
	b1, err := Encode(sample)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Encode(sample)
	if err != nil || string(b1) != string(b2) {
		t.Fatalf("non-deterministic encoding")
	}
	if HasForbiddenObservationKey(b1) {
		t.Fatalf("clean WaybillResult flagged for forbidden observation key")
	}
	if !HasForbiddenObservationKey([]byte(`{"nested":{"raw_html":"<p>x</p>"}}`)) {
		t.Fatalf("failed to detect raw_html key")
	}
}
