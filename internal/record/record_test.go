package record

import "testing"

func TestMusterAndFixtureItemIDs(t *testing.T) {
	valid := []string{"PA-0001", "PA-9999", "PA-10000", "PA-999999999", "itm-0001", "sample-1"}
	for _, id := range valid {
		if !ValidItemID(id) {
			t.Errorf("ValidItemID(%q) = false, want true", id)
		}
	}
	if !IsMusterItemID("PA-0001") || !IsMusterItemID("PA-10000") || IsMusterItemID("PA-01000") || IsMusterItemID("itm-0001") {
		t.Fatal("IsMusterItemID classification wrong")
	}
	invalid := []string{"", "PA-001", "PA-01000", "PA-1000000000", "Bad_ID", "-itm", "itm--1", "itm-"}
	for _, id := range invalid {
		if ValidItemID(id) {
			t.Errorf("ValidItemID(%q) = true, want false", id)
		}
	}
}

func TestPublicHTTPSURL(t *testing.T) {
	good := []string{
		"https://www.ecfr.gov/current/title-48/section-31.205-6",
		"https://csrc.nist.gov/publications/sp800-171",
		"https://rules.example/part-97/section-101",
	}
	for _, u := range good {
		if !PublicHTTPSURL(u) {
			t.Errorf("PublicHTTPSURL(%q) = false, want true", u)
		}
	}
	bad := []string{
		"",
		"http://www.ecfr.gov/current",
		"https://127.0.0.1/test",
		"https://muster.clearedstack.svc.cluster.local:8080/v1/lease",
		"https://localhost/rule",
		"https://user:pass@rules.example/x",
		"https://UPPER.example/x",
		"https://rules.example/x#frag",
	}
	for _, u := range bad {
		if PublicHTTPSURL(u) {
			t.Errorf("PublicHTTPSURL(%q) = true, want false", u)
		}
	}
}

func TestComputeSLA48hAnd5d(t *testing.T) {
	rec := Record{
		State:    StateApproved,
		ToolType: "calculator",
		Determination: Determination{
			Decision:           DecisionOptOut,
			RequestedAt:        "2026-01-05T09:00:00Z",
			IdentityVerifiedAt: "2026-01-06T21:00:00Z", // +36h (within 48h)
			EvaluatedAt:        "2026-01-09T09:00:00Z", // +96h (within 120h)
		},
	}
	sla, err := rec.ComputeSLA()
	if err != nil {
		t.Fatalf("ComputeSLA: %v", err)
	}
	if !sla.VerificationWithinSLA || sla.ResolutionWithinSLA == nil || !*sla.ResolutionWithinSLA || !sla.OptOutActive || !sla.RepublishRequired {
		t.Fatalf("unexpected SLA flags: %+v", sla)
	}
	if sla.VerificationDueAt != "2026-01-07T09:00:00Z" || sla.ResolutionDueAt != "2026-01-10T09:00:00Z" {
		t.Fatalf("unexpected SLA deadlines: %+v", sla)
	}

	// Late verification (>48h) and late resolution (>5d) are accurately flagged.
	recLate := Record{
		State:    StateBuilding,
		ToolType: "calculator",
		Determination: Determination{
			Decision:           DecisionCorrection,
			RequestedAt:        "2026-01-05T09:00:00Z",
			IdentityVerifiedAt: "2026-01-08T09:00:00Z", // +72h (>48h)
			EvaluatedAt:        "2026-01-11T09:00:00Z", // +144h (>120h)
		},
	}
	slaLate, err := recLate.ComputeSLA()
	if err != nil {
		t.Fatalf("ComputeSLA late: %v", err)
	}
	if slaLate.VerificationWithinSLA || slaLate.ResolutionWithinSLA == nil || *slaLate.ResolutionWithinSLA {
		t.Fatalf("expected overdue SLA flags to be false: %+v", slaLate)
	}

	// Missing identity verification on opt_out is refused with E_PARSE.
	recUnverified := Record{
		Determination: Determination{
			Decision:    DecisionOptOut,
			RequestedAt: "2026-01-05T09:00:00Z",
		},
	}
	if _, err := recUnverified.ComputeSLA(); err == nil {
		t.Fatal("expected unverified opt_out to fail")
	}
}
