package eval

import "strings"

// Persona knobs. The frontmatter carries the machine-load-bearing settings; the
// prose is freely human-editable. Defaults are the conservative, honest choices.
const (
	// UnknownRefuse: when asked something the persona doesn't specify, the sim
	// admits it hasn't decided rather than fabricating ground truth (default).
	UnknownRefuse = "refuse"
	// UnknownImprovise: the sim may pick a reasonable answer and stay consistent
	// with it for the rest of the run (seeded fill, never silent inconsistency).
	UnknownImprovise = "improvise_consistent"

	// StyleNaive: a realistic, somewhat passive user who may accept a subtly-wrong
	// answer and stop (default). The success criterion lives in the judge, not the
	// persona, so the sim can be fooled while scoring stays objective.
	StyleNaive = "naive"
	// StyleGoalLocked: a persistent user who pushes until the goal is actually met.
	StyleGoalLocked = "goal_locked"

	// ContextSurface: the sim sees only the user-visible surface (default).
	ContextSurface = "surface"
	// ContextOracle: the sim also sees internals — for deliberate capability-ceiling
	// measurement only; inflates pass rate by papering over UX failures.
	ContextOracle = "oracle"
)

// Persona is the authored user-simulator artifact (persona.md). It decouples a
// golden from any one version's trajectory: the sim answers a NEW version's actual
// questions rather than replaying answers bound to the old version's questions.
type Persona struct {
	OnUnknown string `json:"on_unknown"`
	Style     string `json:"style"`
	Context   string `json:"context"`
	Prose     string `json:"prose"`
}

// ParsePersona reads persona.md: an optional `--- ... ---` YAML-ish frontmatter
// block (one `key: value` per line, trailing `# comments` stripped) followed by
// free prose. Unset knobs fall back to the conservative defaults.
func ParsePersona(md string) Persona {
	p := Persona{OnUnknown: UnknownRefuse, Style: StyleNaive, Context: ContextSurface}
	body := md
	if fm, rest, ok := splitFrontmatter(md); ok {
		body = rest
		for _, line := range strings.Split(fm, "\n") {
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			val = strings.TrimSpace(stripComment(val))
			switch key {
			case "on_unknown":
				if val == UnknownImprovise || val == UnknownRefuse {
					p.OnUnknown = val
				}
			case "style":
				if val == StyleGoalLocked || val == StyleNaive {
					p.Style = val
				}
			case "context":
				if val == ContextOracle || val == ContextSurface {
					p.Context = val
				}
			}
		}
	}
	p.Prose = strings.TrimSpace(body)
	return p
}

// splitFrontmatter returns (frontmatter, body, true) when md opens with a `---`
// fence, else ("", md, false).
func splitFrontmatter(md string) (string, string, bool) {
	s := strings.TrimLeft(md, "\n")
	if !strings.HasPrefix(s, "---") {
		return "", md, false
	}
	s = strings.TrimPrefix(s, "---")
	s = strings.TrimLeft(s, "\r\n")
	end := strings.Index(s, "\n---")
	if end < 0 {
		return "", md, false
	}
	fm := s[:end]
	rest := s[end+len("\n---"):]
	// drop the rest of the closing fence line
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	} else {
		rest = ""
	}
	return fm, rest, true
}

func stripComment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}
