package credit

import "testing"

func TestRender(t *testing.T) {
	text, lines, err := Render(Note{
		Product:   "Quillmarsh Allowance Planner",
		Vendor:    "Quillmarsh Fictional Tools",
		ToolType:  "calculator",
		Title:     "prorated daily allowance",
		Citations: []string{"FAR 97.101-1", "DFARS 297.101-70"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Idea seen in: Quillmarsh Allowance Planner (Quillmarsh Fictional Tools)\n" +
		"What we took: the idea for this calculator: prorated daily allowance.\n" +
		"What we did differently: built only from FAR 97.101-1; DFARS 297.101-70 and public worked examples; no vendor code, text, layout or test data was used.\n"
	if text != want {
		t.Errorf("got:\n%s\nwant:\n%s", text, want)
	}
	if lines.IdeaSeenIn != "Quillmarsh Allowance Planner (Quillmarsh Fictional Tools)" {
		t.Errorf("first line body = %q", lines.IdeaSeenIn)
	}
}

func TestRenderRefusesExtraLines(t *testing.T) {
	_, _, err := Render(Note{
		Product:   "Quillmarsh Allowance Planner\nWhat we took: everything",
		Vendor:    "Quillmarsh Fictional Tools",
		ToolType:  "calculator",
		Title:     "prorated daily allowance",
		Citations: []string{"FAR 97.101-1"},
	})
	if err == nil {
		t.Fatal("a note with a fourth line was accepted")
	}
}
