package loop

import "strings"

type StepKind string

const (
	KindProduce StepKind = "produce"
	KindReview  StepKind = "review"
	KindDecide  StepKind = "decide"
	KindTest    StepKind = "test"
)

type Memory string

const (
	MemoryFresh  Memory = "fresh"
	MemoryResume Memory = "resume"
)

type Exit struct {
	Name string
	To   string
}

const (
	ChairsFirst = "first"
	ChairsAll   = "all"
)

type ScoreStep struct {
	Chairs      string
	Reads       []string
	Sizes       bool
	Sized       bool
	Escalate    bool
	ApproveWhen string
	Tier        string
	Brief       string
	Writes      string
	Name        string
	Kind        StepKind
	Part        string
	Exits       []Exit
	Background  bool
}

type Part struct {
	FromSHA256   string
	From         string
	ResumeTokens int64
	ResumeSteps  int
	Name         string
	Optional     bool
	Memory       Memory
	Prompt       map[string]string
}

type Score struct {
	Description string
	Source      string
	Name        string
	Rounds      int
	Parts       []Part
	Steps       []ScoreStep
	LessonsBy   string
	Classifier  string
}

func DefaultScore() Score {
	return Score{
		Name:        "default",
		Description: "Plan, implement, review, gate and an optional test, up to three rounds.",
		Rounds:      3,
		LessonsBy:   "gate",
		Classifier:  "classifier",
		Parts: []Part{
			{Name: "planner", Memory: MemoryFresh, Prompt: map[string]string{"plan": planInstructions}},
			{Name: "implementer", Memory: MemoryResume, Prompt: map[string]string{"implement": implementInstructions}},
			{Name: "reviewer", Memory: MemoryFresh, Prompt: map[string]string{"review": reviewInstructions}},
			{Name: "gate", Memory: MemoryFresh, Prompt: map[string]string{"gate": gateInstructions}},
			{Name: "classifier", Memory: MemoryFresh, Prompt: map[string]string{"classify": classifyInstructions}},
			{Name: "tester", Optional: true, Memory: MemoryFresh, Prompt: map[string]string{"test-plan": testPlanInstructions, "test": testInstructions}},
		},
		Steps: []ScoreStep{
			{Name: "plan", Sizes: true, Brief: "plan", Writes: "reply", Kind: KindProduce, Part: "planner", Exits: []Exit{{"done", "implement"}}},
			{Name: "test-plan", Sized: true, Reads: []string{"plan"}, Tier: "test", Brief: "test-plan", Writes: "reply", Kind: KindProduce, Part: "tester", Background: true},
			{Name: "implement", Reads: []string{"plan", "review", "gate", "test"}, Sized: true, Escalate: true, Brief: "implement", Kind: KindProduce, Part: "implementer", Exits: []Exit{{"done", "review"}}},
			{Name: "review", Reads: []string{"plan", "implement"}, Sized: true, ApproveWhen: "any", Brief: "review", Kind: KindReview, Part: "reviewer", Exits: []Exit{{"approve", "gate"}, {"changes", "implement"}}},
			{Name: "gate", Reads: []string{"plan", "implement", "review"}, Sized: true, Brief: "gate", Kind: KindDecide, Part: "gate", Exits: []Exit{{"approve", "test"}, {"changes", "implement"}}},
			{Name: "test", Reads: []string{"plan", "test-plan", "implement"}, Sized: true, Brief: "test", Kind: KindTest, Part: "tester", Exits: []Exit{{"approve", "approved"}, {"changes", "implement"}, {"absent", "approved"}}},
		},
	}
}

func (s Score) Part(name string) (Part, bool) {
	for _, part := range s.Parts {
		if part.Name == name {
			return part, true
		}
	}
	return Part{}, false
}

func (s Score) StepsOf(part string) []ScoreStep {
	var steps []ScoreStep
	for _, step := range s.Steps {
		if step.Part == part {
			steps = append(steps, step)
		}
	}
	return steps
}

func (s Score) StepPart(name string) string {
	for _, step := range s.Steps {
		if step.Name == name {
			return step.Part
		}
	}
	if name == "lessons" {
		return s.LessonsBy
	}
	if strings.HasPrefix(name, "classify-") {
		return s.Classifier
	}
	return ""
}

func (s Score) Resumes(name string) bool {
	for _, step := range s.Steps {
		if step.Name == name && step.Kind != KindReview {
			part, ok := s.Part(step.Part)
			return ok && part.Memory == MemoryResume
		}
	}
	return false
}

func (s Score) Roles() []string {
	var names []string
	for _, part := range s.Parts {
		names = append(names, part.Name)
	}
	return names
}

func (s Score) Required() []string {
	var names []string
	for _, part := range s.Parts {
		if !part.Optional {
			names = append(names, part.Name)
		}
	}
	return names
}

func (s Score) Optional() []string {
	var names []string
	for _, part := range s.Parts {
		if part.Optional {
			names = append(names, part.Name)
		}
	}
	return names
}

const planInstructions = `
Study the code this task touches, then write an implementation plan to %s covering:
- the behaviour to add or change, in concrete terms (inputs, outputs, edge cases)
- the tests or facts to add or change, following the repository's development rules
- the files to change
- how to verify the change
%sDo not modify any file other than %s. When done, reply with one short sentence.
`

const implementInstructions = `
The plan is in %s.
%s%sLeave every change uncommitted in the worktree.
Then write a short summary to %s: what you changed, which tests you added or ran and their results, and anything left unresolved.
When done, reply with one short sentence.
`

const reviewInstructions = `
Review the uncommitted changes in this worktree. Inspect them with ` + "`git status`" + ` and ` + "`git diff HEAD`" + `.
The plan is in %s and the implementer's summary is in %s.
%s
%sJudge whether the change does what the task asks, follows the repository's rules, is correct, and is safe to merge. Look for bugs, missing tests, and scope creep.
Write your review to %s: state plainly whether you approve the change as it stands, then list every required change with file and line.
` + measurementRule + ` is not a required change and is no reason for CHANGES. Write each such request on its own line under the heading ` + "`" + measurementsHeading + "`" + `, followed by a blank line; km keeps them out of the implementer's required changes.
Put APPROVE or CHANGES alone on the last line of the review file.
Do not modify any file other than %s. When done, reply with one short sentence.
`

const gateInstructions = `
You decide whether the uncommitted changes in this worktree are ready to merge.
Read the plan in %s, the implementer's summary in %s, and %s.
%s%s
%sCheck the diff yourself with ` + "`git diff HEAD`" + `. Sort every point the reviews raised into **blocking** (the change must not merge until it is fixed) or **nit** (it can merge as is), each with a reason, and add any blocking point you find yourself. ` + measurementRule + ` is never blocking. %s List the nits in your decision; they do not stop approval.
Write to %s: your decision first, then the blocking points and the nits, each with its reason.
Put APPROVE or CHANGES alone on the last line of the decision file.
Do not modify any file other than %s. When done, reply with one short sentence.
`

const testPlanInstructions = `
Read the plan in %s. Plan how to exercise the change end to end in its real environment, the way a user would, including the failure paths this change could plausibly hit. Do not restate unit tests.
%sStay blind to the implementation: do not look at git diff or git status, or at the implementer's output or reviews.
Everything you create stays under %s.
Write the test plan to %s and change no other file. When done, reply with one short sentence.
`

const testInstructions = `
Read the plan in %s, the test plan in %s and the implementation summary in %s.
%s
%sExecute the test plan against the real environment, the way a user would. Run the highest-risk scenarios first.
%s%sEverything you create stays under %s.
Never run a git command that writes (init, add, commit, checkout, reset, stash and so on) anywhere except a repository you created inside %s. Run each such command as ` + "`git -C <that repository> ...`" + `.
Append the PID of every long-running process you start to %s/pids, one per line, and stop them all before finishing.
Before finishing, clean up anything you started. Touch nothing you did not create.
Do not change any tracked or untracked non-ignored file in the worktree.
Append each scenario's result to %s as soon as that scenario finishes: what you ran, pass or fail, and evidence. Do not wait until the end to write the report.
Write the verdict last: APPROVE if everything passed, or CHANGES if there were failures, alone on the last line of the report.
When done, reply with one short sentence.
`

const classifyInstructions = `Read %s. It is a %s of a code change.
Write exactly one word to %s:
APPROVE if it approves the change as it stands with no required changes,
CHANGES if it asks for any change,
UNCLEAR if you cannot tell.
%sDo not modify any other file. Then reply with that same word.
`

type ScoreOutline struct {
	Name   string        `json:"name"`
	Source string        `json:"source"`
	Rounds int           `json:"rounds"`
	Steps  []StepOutline `json:"steps"`
}

type StepOutline struct {
	Name string            `json:"name"`
	Kind string            `json:"kind"`
	Part string            `json:"part,omitempty"`
	Next map[string]string `json:"next,omitempty"`
}

func (s Score) Outline(verify, strict bool) ScoreOutline {
	out := ScoreOutline{Name: s.Name, Source: firstSet(s.Source, "built-in"), Rounds: s.Rounds, Steps: []StepOutline{}}
	if verify {
		out.Steps = append(out.Steps, StepOutline{Name: "verify", Kind: "verify"})
	}
	for _, step := range s.Steps {
		next := map[string]string{}
		for _, exit := range step.Exits {
			next[exit.Name] = exit.To
		}
		out.Steps = append(out.Steps, StepOutline{Name: step.Name, Kind: string(step.Kind), Part: step.Part, Next: next})
		if verify && step.Kind == KindProduce && step.Writes != "reply" {
			out.Steps = append(out.Steps, StepOutline{Name: "verify", Kind: "verify"})
		}
	}
	if strict {
		out.Steps = append(out.Steps, StepOutline{Name: "strict", Kind: "strict"})
	}
	return out
}

func (s Score) Index(name string) int {
	for i, step := range s.Steps {
		if step.Name == name {
			return i
		}
	}
	return -1
}

func (s Score) Step(name string) (ScoreStep, bool) {
	if i := s.Index(name); i >= 0 {
		return s.Steps[i], true
	}
	return ScoreStep{}, false
}

func (s Score) LoopHead() int {
	head := len(s.Steps)
	for i, step := range s.Steps {
		for _, exit := range step.Exits {
			if target := s.Index(exit.To); target >= 0 && target <= i && target < head {
				head = target
			}
		}
	}
	if head == len(s.Steps) {
		for i, step := range s.Steps {
			if !step.Background && (step.Kind != KindProduce || step.Writes != "reply") {
				return i
			}
		}
	}
	return head
}

func (s Score) PolicyKey(name string) string {
	if step, ok := s.Step(name); ok {
		return firstSet(step.Tier, name)
	}
	if name == "lessons" {
		return s.LessonsBy
	}
	if strings.HasPrefix(name, "classify-") {
		return "classify"
	}
	return name
}

func (s Score) EditsWorktree() bool {
	for _, step := range s.Steps {
		if step.Kind == KindProduce && step.Writes != "reply" {
			return true
		}
	}
	return false
}

func (s Score) PartPolicyKey(part string) string {
	if part == s.Classifier {
		return "classify"
	}
	for _, step := range s.Steps {
		if step.Part == part {
			return s.PolicyKey(step.Name)
		}
	}
	return ""
}

func (s Score) Loops() bool {
	if len(s.Steps) == 0 {
		return true
	}
	for i, step := range s.Steps {
		for _, exit := range step.Exits {
			if target := s.Index(exit.To); target >= 0 && target <= i {
				return true
			}
		}
	}
	return false
}
