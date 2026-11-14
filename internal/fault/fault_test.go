package fault

import "testing"

func TestFaultCodesAndFingerprints(t *testing.T) {
	p1 := Broken("authority[0].source", "the authority is not one of the official publications")
	p2 := Broken("authority[0].source", "the authority is not one of the official publications")
	p3 := Broken("authority[1].source", "the authority is not one of the official publications")
	u := Unfamiliar("tool_type", "no spec-profile covers this tool type")
	s := Slow("", "upstream call timed out")

	if p1.Code != Parse || u.Code != NeedsSubtool || s.Code != Timeout {
		t.Fatalf("unexpected codes: %q %q %q", p1.Code, u.Code, s.Code)
	}
	if p1.Fingerprint() == "" || len(p1.Fingerprint()) != 16 {
		t.Fatalf("unexpected fingerprint %q", p1.Fingerprint())
	}
	if p1.Fingerprint() != p2.Fingerprint() {
		t.Fatalf("identical errors produced different fingerprints: %q != %q", p1.Fingerprint(), p2.Fingerprint())
	}
	if p1.Fingerprint() == p3.Fingerprint() || p1.Fingerprint() == u.Fingerprint() {
		t.Fatalf("distinct errors collided: %q, %q, %q", p1.Fingerprint(), p3.Fingerprint(), u.Fingerprint())
	}
	if s.Error() != "E_TIMEOUT: upstream call timed out" {
		t.Fatalf("unexpected string: %q", s.Error())
	}
	var nilErr *Error
	if nilErr.Error() != "" || nilErr.Fingerprint() != "" {
		t.Fatalf("nil receiver not safe")
	}
}
