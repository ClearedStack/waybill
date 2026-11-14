package decisiontree

import (
	"fmt"
	"strconv"

	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/record"
	"github.com/ClearedStack/waybill/internal/strictjson"
)

const (
	kindChoice   = "choice"
	maxQuestions = 40
	maxOptions   = 10
	maxOutcomes  = 20
	maxExamples  = 50
)

type tool struct {
	Start          string     `json:"start"`
	Questions      []Question `json:"questions"`
	Outcomes       []Outcome  `json:"outcomes"`
	WorkedExamples []example  `json:"worked_examples"`
}

// Option is one selectable answer on a decision-tree question.
type Option struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Next  string `json:"next"`
}

// Question is one branching node in a decision-tree tool.
type Question struct {
	ID       string   `json:"id"`
	Prompt   string   `json:"prompt"`
	Help     string   `json:"help,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Citation string   `json:"citation"`
	Options  []Option `json:"options"`
}

// Outcome is one terminal determination in a decision-tree tool.
type Outcome struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Status   string `json:"status"`
	Citation string `json:"citation"`
}

type example struct {
	ID        string            `json:"id,omitempty"`
	Label     string            `json:"label,omitempty"`
	Authority string            `json:"authority"`
	Answers   map[string]string `json:"answers"`
	Expected  Expected          `json:"expected"`
}

// Expected is the verified terminal outcome of one worked example.
type Expected struct {
	Outcome string `json:"outcome"`
	Status  string `json:"status"`
	Steps   int    `json:"steps"`
}

// Schema is the "schema" part of a decision-tree build specification.
type Schema struct {
	ToolType  string     `json:"tool_type"`
	Title     string     `json:"title"`
	Start     string     `json:"start"`
	Questions []Question `json:"questions"`
	Outcomes  []Outcome  `json:"outcomes"`
}

// Vector is one public worked example for a decision-tree tool.
type Vector struct {
	Authority string            `json:"authority"`
	Answers   map[string]string `json:"inputs"`
	Expected  Expected          `json:"expected"`
}

// MoldloftBody is the pattern-specific body for Moldloft's decision-tree template.
type MoldloftBody struct {
	Start     string     `json:"start"`
	Questions []Question `json:"questions"`
	Outcomes  []Outcome  `json:"outcomes"`
}

// MoldloftExample is one worked example in Moldloft's decision-tree format.
type MoldloftExample struct {
	ID      string            `json:"id"`
	Label   string            `json:"label"`
	Answers map[string]string `json:"answers"`
	Expect  Expected          `json:"expect"`
}

// Build turns a decision-tree record into its build-specification schema and vectors.
func Build(rec *record.Record) (*Schema, []Vector, *fault.Error) {
	schema, vectors, _, _, err := BuildFull(rec)
	return schema, vectors, err
}

// BuildFull turns a decision-tree record into both Waybill's schema/vectors and
// Moldloft's decision-tree body/worked_examples. Cycles, dangling targets,
// unreachable questions or outcomes, and disagreeing examples are refused.
func BuildFull(rec *record.Record) (*Schema, []Vector, *MoldloftBody, []MoldloftExample, *fault.Error) {
	var t tool
	if err := strictjson.Decode(rec.Tool, &t); err != nil {
		return nil, nil, nil, nil, fault.Broken("tool", "the tool description is not valid JSON of the expected shape")
	}
	if !record.NameRE.MatchString(t.Start) {
		return nil, nil, nil, nil, fault.Broken("tool.start", "the start question id is missing or malformed")
	}
	if len(t.Questions) == 0 || len(t.Questions) > maxQuestions {
		return nil, nil, nil, nil, fault.Broken("tool.questions", "the number of questions is out of range")
	}
	if len(t.Outcomes) == 0 || len(t.Outcomes) > maxOutcomes {
		return nil, nil, nil, nil, fault.Broken("tool.outcomes", "the number of outcomes is out of range")
	}

	authorities := map[string]bool{}
	for _, a := range rec.Authority {
		authorities[a.ID] = true
	}

	questionsByID := map[string]*Question{}
	var cleanQuestions []Question
	for i, q := range t.Questions {
		at := "tool.questions[" + strconv.Itoa(i) + "]"
		if !record.NameRE.MatchString(q.ID) || questionsByID[q.ID] != nil {
			return nil, nil, nil, nil, fault.Broken(at+".id", "the question id is missing, malformed or repeated")
		}
		if !record.TextRunes(q.Prompt, 240) {
			return nil, nil, nil, nil, fault.Broken(at+".prompt", "the question prompt is missing or malformed")
		}
		if q.Help != "" && !record.TextRunes(q.Help, 240) {
			return nil, nil, nil, nil, fault.Broken(at+".help", "the question help text is malformed")
		}
		kind := q.Kind
		if kind == "" {
			kind = kindChoice
		} else if !record.NameRE.MatchString(kind) {
			return nil, nil, nil, nil, fault.Broken(at+".kind", "the question kind is malformed")
		}
		if kind != kindChoice {
			return nil, nil, nil, nil, fault.Unfamiliar(at+".kind", "the decision-tree profile does not cover this question kind")
		}
		if !authorities[q.Citation] {
			return nil, nil, nil, nil, fault.Broken(at+".citation", "the question citation does not name one of the record's authorities")
		}
		if len(q.Options) < 2 || len(q.Options) > maxOptions {
			return nil, nil, nil, nil, fault.Broken(at+".options", "the number of options is out of range")
		}
		seenOpt := map[string]bool{}
		var cleanOpts []Option
		for j, opt := range q.Options {
			oat := at + ".options[" + strconv.Itoa(j) + "]"
			if !record.NameRE.MatchString(opt.Key) || seenOpt[opt.Key] {
				return nil, nil, nil, nil, fault.Broken(oat+".key", "the option key is missing, malformed or repeated")
			}
			if !record.TextRunes(opt.Label, 120) {
				return nil, nil, nil, nil, fault.Broken(oat+".label", "the option label is missing or malformed")
			}
			if !record.NameRE.MatchString(opt.Next) {
				return nil, nil, nil, nil, fault.Broken(oat+".next", "the option next target is missing or malformed")
			}
			seenOpt[opt.Key] = true
			cleanOpts = append(cleanOpts, opt)
		}
		cleanQ := Question{
			ID:       q.ID,
			Prompt:   q.Prompt,
			Help:     q.Help,
			Kind:     kind,
			Citation: q.Citation,
			Options:  cleanOpts,
		}
		cleanQuestions = append(cleanQuestions, cleanQ)
		qCopy := cleanQ
		questionsByID[q.ID] = &qCopy
	}

	outcomesByID := map[string]*Outcome{}
	var cleanOutcomes []Outcome
	for i, out := range t.Outcomes {
		at := "tool.outcomes[" + strconv.Itoa(i) + "]"
		if !record.NameRE.MatchString(out.ID) || questionsByID[out.ID] != nil || outcomesByID[out.ID] != nil {
			return nil, nil, nil, nil, fault.Broken(at+".id", "the outcome id is missing, malformed or repeated")
		}
		if !record.TextRunes(out.Title, 120) {
			return nil, nil, nil, nil, fault.Broken(at+".title", "the outcome title is missing or malformed")
		}
		if !record.TextRunes(out.Summary, 300) {
			return nil, nil, nil, nil, fault.Broken(at+".summary", "the outcome summary is missing or malformed")
		}
		if out.Status != "eligible" && out.Status != "ineligible" && out.Status != "review" {
			return nil, nil, nil, nil, fault.Broken(at+".status", "the outcome status must be eligible, ineligible or review")
		}
		if !authorities[out.Citation] {
			return nil, nil, nil, nil, fault.Broken(at+".citation", "the outcome citation does not name one of the record's authorities")
		}
		cleanOutcomes = append(cleanOutcomes, out)
		outCopy := out
		outcomesByID[out.ID] = &outCopy
	}

	if questionsByID[t.Start] == nil {
		return nil, nil, nil, nil, fault.Broken("tool.start", "the start id does not name one of the declared questions")
	}
	for i, q := range cleanQuestions {
		for j, opt := range q.Options {
			if questionsByID[opt.Next] == nil && outcomesByID[opt.Next] == nil {
				return nil, nil, nil, nil, fault.Broken(
					"tool.questions["+strconv.Itoa(i)+"].options["+strconv.Itoa(j)+"].next",
					"the option target does not name a question or outcome",
				)
			}
		}
	}

	// Verify acyclicity and full reachability from start.
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	qState := map[string]int{}
	reachedOutcomes := map[string]bool{}
	var dfs func(id string) *fault.Error
	dfs = func(id string) *fault.Error {
		switch qState[id] {
		case visiting:
			return fault.Broken("tool.questions", "the decision graph contains a cycle")
		case visited:
			return nil
		}
		qState[id] = visiting
		for _, opt := range questionsByID[id].Options {
			if outcomesByID[opt.Next] != nil {
				reachedOutcomes[opt.Next] = true
				continue
			}
			if err := dfs(opt.Next); err != nil {
				return err
			}
		}
		qState[id] = visited
		return nil
	}
	if err := dfs(t.Start); err != nil {
		return nil, nil, nil, nil, err
	}
	for i, q := range cleanQuestions {
		if qState[q.ID] != visited {
			return nil, nil, nil, nil, fault.Broken("tool.questions["+strconv.Itoa(i)+"].id", "the question is not reachable from start")
		}
	}
	for i, out := range cleanOutcomes {
		if !reachedOutcomes[out.ID] {
			return nil, nil, nil, nil, fault.Broken("tool.outcomes["+strconv.Itoa(i)+"].id", "the outcome is not reachable from start")
		}
	}

	if len(t.WorkedExamples) == 0 || len(t.WorkedExamples) > maxExamples {
		return nil, nil, nil, nil, fault.Broken("tool.worked_examples", "the number of worked examples is out of range")
	}
	var vectors []Vector
	var mExamples []MoldloftExample
	seenExIDs := map[string]bool{}
	for i, ex := range t.WorkedExamples {
		at := "tool.worked_examples[" + strconv.Itoa(i) + "]"
		if !authorities[ex.Authority] {
			return nil, nil, nil, nil, fault.Broken(at+".authority", "the worked example does not name one of the record's authorities")
		}
		exID := ex.ID
		if exID == "" {
			exID = fmt.Sprintf("example_%d", i+1)
		} else if !record.NameRE.MatchString(exID) {
			return nil, nil, nil, nil, fault.Broken(at+".id", "the worked example id is malformed")
		}
		if seenExIDs[exID] {
			return nil, nil, nil, nil, fault.Broken(at+".id", "the worked example id is repeated")
		}
		seenExIDs[exID] = true

		exLabel := ex.Label
		if exLabel == "" {
			exLabel = fmt.Sprintf("Worked example %d (%s)", i+1, ex.Authority)
		} else if !record.TextRunes(exLabel, 160) {
			return nil, nil, nil, nil, fault.Broken(at+".label", "the worked example label is malformed")
		}

		cleanAnswers := make(map[string]string, len(ex.Answers))
		for k, v := range ex.Answers {
			if questionsByID[k] == nil {
				return nil, nil, nil, nil, fault.Broken(at+".answers."+k, "answers a question that is not in this decision tree")
			}
			if !record.NameRE.MatchString(v) {
				return nil, nil, nil, nil, fault.Broken(at+".answers."+k, "the option answer is malformed")
			}
			cleanAnswers[k] = v
		}

		got, visitedQ, ok := evaluateTree(t.Start, questionsByID, outcomesByID, cleanAnswers)
		if !ok {
			return nil, nil, nil, nil, fault.Broken(at+".answers", "the worked example does not reach an outcome cleanly")
		}
		for k := range cleanAnswers {
			if !visitedQ[k] {
				return nil, nil, nil, nil, fault.Broken(at+".answers."+k, "answers a question not visited along this branch")
			}
		}
		if got != ex.Expected {
			return nil, nil, nil, nil, fault.Broken(at+".expected", "the decision-tree evaluation does not match the expected outcome")
		}

		vectors = append(vectors, Vector{
			Authority: ex.Authority,
			Answers:   cleanAnswers,
			Expected:  got,
		})
		mExamples = append(mExamples, MoldloftExample{
			ID:      exID,
			Label:   exLabel,
			Answers: cleanAnswers,
			Expect:  got,
		})
	}

	schema := &Schema{
		ToolType:  ToolType,
		Title:     rec.Title,
		Start:     t.Start,
		Questions: cleanQuestions,
		Outcomes:  cleanOutcomes,
	}
	mBody := &MoldloftBody{
		Start:     t.Start,
		Questions: cleanQuestions,
		Outcomes:  cleanOutcomes,
	}
	return schema, vectors, mBody, mExamples, nil
}

func evaluateTree(start string, questions map[string]*Question, outcomes map[string]*Outcome, answers map[string]string) (Expected, map[string]bool, bool) {
	visited := map[string]bool{}
	cur := start
	steps := 0
	for {
		if visited[cur] {
			return Expected{}, nil, false
		}
		visited[cur] = true
		q := questions[cur]
		if q == nil {
			return Expected{}, nil, false
		}
		ans := answers[cur]
		if ans == "" {
			return Expected{}, nil, false
		}
		var matched *Option
		for i := range q.Options {
			if q.Options[i].Key == ans {
				matched = &q.Options[i]
				break
			}
		}
		if matched == nil {
			return Expected{}, nil, false
		}
		steps++
		if out := outcomes[matched.Next]; out != nil {
			return Expected{
				Outcome: out.ID,
				Status:  out.Status,
				Steps:   steps,
			}, visited, true
		}
		cur = matched.Next
	}
}
