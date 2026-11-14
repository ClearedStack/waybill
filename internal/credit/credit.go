// Package credit renders the three-line credit note.
//
// The wording lives in credit-note.tmpl (and credit-note-neutral.tmpl when a
// verified nominative attribution opt-out is active) and is the same for every
// item. Only names or neutral descriptors, the tool type, the title and the
// citations are filled in, so a note states facts and cannot rate a product or
// suggest a partnership.
package credit

import (
	_ "embed"
	"errors"
	"strings"
	"text/template"
)

//go:embed credit-note.tmpl
var source string

//go:embed credit-note-neutral.tmpl
var neutralSource string

var (
	tmpl = template.Must(template.New("credit-note").
		Option("missingkey=error").
		Funcs(template.FuncMap{"join": strings.Join}).
		Parse(source))

	neutralTmpl = template.Must(template.New("credit-note-neutral").
			Option("missingkey=error").
			Funcs(template.FuncMap{"join": strings.Join}).
			Parse(neutralSource))
)

// Labels are the three lines of a note, in order.
var Labels = [3]string{"Idea seen in", "What we took", "What we did differently"}

// Note is what a credit note is filled in with.
type Note struct {
	Product   string
	Vendor    string
	Neutral   string
	OptOut    bool
	ToolType  string
	Title     string
	Citations []string
}

// Lines is a rendered note, one field per line, without the labels.
type Lines struct {
	IdeaSeenIn           string
	WhatWeTook           string
	WhatWeDidDifferently string
}

// ErrShape means the rendered note was not exactly the three expected lines.
var ErrShape = errors.New("credit: the note is not three labelled lines")

// Render returns the note as text ending in a newline, and its three lines.
func Render(n Note) (string, Lines, error) {
	chosen := tmpl
	if n.OptOut {
		if strings.TrimSpace(n.Neutral) == "" {
			return "", Lines{}, ErrShape
		}
		chosen = neutralTmpl
	}
	var b strings.Builder
	if err := chosen.Execute(&b, n); err != nil {
		return "", Lines{}, ErrShape
	}
	text := b.String()
	parts := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(parts) != len(Labels) || !strings.HasSuffix(text, "\n") {
		return "", Lines{}, ErrShape
	}
	var body [3]string
	for i, label := range Labels {
		rest, ok := strings.CutPrefix(parts[i], label+": ")
		if !ok || rest == "" {
			return "", Lines{}, ErrShape
		}
		body[i] = rest
	}
	return text, Lines{body[0], body[1], body[2]}, nil
}
