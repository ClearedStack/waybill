package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtures = "../../tools/spec-profiles/calculator/testdata"

func TestCompileFromFile(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"compile", "-out", out, filepath.Join(fixtures, "ok-capped-allowance", "record.json")},
		strings.NewReader(""), &stdout, &stderr)
	if code != exitOK || stderr.Len() != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"state": "approved"`) {
		t.Errorf("stdout: %s", stdout.String())
	}
	for written, golden := range map[string]string{
		"itm-0001.credit-note.txt": "credit-note.golden.txt",
		"itm-0001.build-spec.json": "build-spec.golden.json",
	} {
		got, err := os.ReadFile(filepath.Join(out, written))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(fixtures, "ok-capped-allowance", golden))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s", written, golden)
		}
	}
}

func TestCompileFromStdinIsRepeatable(t *testing.T) {
	input, err := os.ReadFile(filepath.Join(fixtures, "ok-two-outputs-two-authorities", "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var first, second, stderr bytes.Buffer
	if code := run([]string{"compile"}, bytes.NewReader(input), &first, &stderr); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if code := run([]string{"compile", "-"}, bytes.NewReader(input), &second, &stderr); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Error("two runs gave different output")
	}
}

func TestFailureWritesNoFiles(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"compile", "-out", out, filepath.Join(fixtures, "needs-subtool-tool-type", "record.json")},
		strings.NewReader(""), &stdout, &stderr)
	if code != exitFault {
		t.Fatalf("exit %d", code)
	}
	want, err := os.ReadFile(filepath.Join(fixtures, "needs-subtool-tool-type", "error.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Errorf("stdout: %s", stdout.String())
	}
	if left, _ := os.ReadDir(out); len(left) != 0 {
		t.Errorf("%d files written for a failed item", len(left))
	}
}

// An error must not name the machine: a missing file is reported without its
// path.
func TestUnreadableFileIsSanitized(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"compile", "/no-such-host-dir/secret-name.json"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitFault || !strings.Contains(stdout.String(), `"code": "E_PARSE"`) {
		t.Fatalf("exit %d, stdout %s", code, stdout.String())
	}
	if all := stdout.String() + stderr.String(); strings.Contains(all, "secret-name") || strings.Contains(all, "no-such-host-dir") {
		t.Errorf("the path leaked: %s", all)
	}
}

func TestOversizedRecord(t *testing.T) {
	var stdout, stderr bytes.Buffer
	big := strings.NewReader(`{"title":"` + strings.Repeat("a", 1<<20) + `"}`)
	if code := run([]string{"compile"}, big, &stdout, &stderr); code != exitFault ||
		!strings.Contains(stdout.String(), "larger than the limit") {
		t.Fatalf("exit %d, stdout %s", code, stdout.String())
	}
}

func TestRunStandinOnce(t *testing.T) {
	rawRec, err := os.ReadFile(filepath.Join(fixtures, "ok-opt-out-neutral", "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(rawRec, &rec); err != nil {
		t.Fatal(err)
	}
	seed := []map[string]any{
		{
			"id":    "PA-0042",
			"state": "building",
			"lane":  "urgent",
			"next":  "waybill",
			"results": map[string]any{
				"countersign": map[string]any{
					"outcome": "approve",
					"waybill": rec,
				},
			},
		},
	}
	seedBytes, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	seedFile := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(seedFile, seedBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-standin", "-seed", seedFile, "-once", "-spool", t.TempDir()},
		strings.NewReader(""), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), `"event":"delivered"`) || !strings.Contains(stderr.String(), `"item_id":"PA-0042"`) {
		t.Errorf("expected delivered log line, got %q", stderr.String())
	}
}

func TestRunUnknownContractVersionRefused(t *testing.T) {
	t.Setenv("CONTRACT_VERSION", "v99")
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-standin", "-once", "-spool", t.TempDir()}, strings.NewReader(""), &stdout, &stderr)
	if code != exitFault || !strings.Contains(stdout.String(), `"code": "E_NEEDS_SUBTOOL"`) {
		t.Fatalf("exit %d, stdout=%q", code, stdout.String())
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"build"}, {"compile", "-nope"}, {"compile", "a", "b"}, {"run", "extra"}, {"version", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, strings.NewReader(""), &stdout, &stderr); code != exitUsage || stdout.Len() != 0 {
			t.Errorf("%v: exit %d, stdout %q", args, code, stdout.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, nil, &stdout, &stderr); code != exitOK || stdout.String() != "waybill v0.1.0-draft\n" {
		t.Errorf("version: exit %d, %q", code, stdout.String())
	}
}
