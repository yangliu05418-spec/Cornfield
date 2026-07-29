package promptrefiner

import (
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

//go:embed rules.yaml
var embeddedRules []byte

type Source struct {
	Type      string `yaml:"type" json:"type"`
	Reference string `yaml:"reference" json:"reference"`
	License   string `yaml:"license,omitempty" json:"license,omitempty"`
}

type Rule struct {
	ID            string   `yaml:"id"`
	Locale        string   `yaml:"locale"`
	TranslationOf string   `yaml:"translation_of,omitempty"`
	Source        Source   `yaml:"source"`
	Category      string   `yaml:"category"`
	Mode          string   `yaml:"mode"`
	Terms         []string `yaml:"terms"`
	Exceptions    []string `yaml:"exceptions,omitempty"`
	Reason        string   `yaml:"reason"`
	Replacements  []string `yaml:"replacements,omitempty"`
}

type rulesFile struct {
	Version string `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

type Finding struct {
	ID           string   `json:"id"`
	RuleID       string   `json:"-"`
	Locale       string   `json:"locale"`
	Category     string   `json:"category"`
	Mode         string   `json:"mode"`
	Original     string   `json:"original"`
	Reason       string   `json:"reason"`
	Replacements []string `json:"replacements,omitempty"`
	Start        int      `json:"-"`
	End          int      `json:"-"`
}

type Segment struct {
	Text      string `json:"text"`
	FindingID string `json:"finding_id,omitempty"`
}

type Result struct {
	PolicyVersion string    `json:"policy_version"`
	Status        string    `json:"status"`
	Segments      []Segment `json:"segments"`
	Findings      []Finding `json:"findings"`
}

type termOutput struct {
	ruleIndex int
	termRunes int
	term      string
}

type node struct {
	next    map[rune]int
	fail    int
	outputs []termOutput
}

type Engine struct {
	version string
	rules   []Rule
	nodes   []node
}

func New() (*Engine, error) {
	var file rulesFile
	if err := yaml.Unmarshal(embeddedRules, &file); err != nil {
		return nil, fmt.Errorf("decode prompt rules: %w", err)
	}
	if err := validate(file); err != nil {
		return nil, err
	}
	engine := &Engine{version: file.Version, rules: file.Rules, nodes: []node{{next: map[rune]int{}}}}
	for ruleIndex, rule := range engine.rules {
		for _, term := range rule.Terms {
			engine.insert(normalizeRunes(term), termOutput{ruleIndex: ruleIndex, termRunes: len(normalizeRunes(term)), term: term})
		}
	}
	engine.buildFailures()
	return engine, nil
}

func validate(file rulesFile) error {
	if strings.TrimSpace(file.Version) == "" || len(file.Rules) == 0 {
		return errors.New("prompt rules require a version and at least one rule")
	}
	ids := make(map[string]int, len(file.Rules))
	for index, rule := range file.Rules {
		if rule.ID == "" || ids[rule.ID] > 0 {
			return fmt.Errorf("prompt rule %d has a blank or duplicate id", index)
		}
		ids[rule.ID]++
		if rule.Locale != "en" && rule.Locale != "zh" {
			return fmt.Errorf("prompt rule %s has unsupported locale", rule.ID)
		}
		if rule.Source.Type == "" || rule.Source.Reference == "" || rule.Category == "" || rule.Reason == "" || len(rule.Terms) == 0 {
			return fmt.Errorf("prompt rule %s is incomplete", rule.ID)
		}
		if rule.Source.Type == "community" && rule.Source.License == "" {
			return fmt.Errorf("community prompt rule %s requires a license", rule.ID)
		}
		if rule.Mode != "mapped" && rule.Mode != "contextual" && rule.Mode != "manual_only" {
			return fmt.Errorf("prompt rule %s has invalid mode", rule.ID)
		}
		if rule.Mode == "mapped" && len(rule.Replacements) == 0 || rule.Mode == "manual_only" && len(rule.Replacements) > 0 {
			return fmt.Errorf("prompt rule %s has replacements inconsistent with its mode", rule.ID)
		}
		for _, value := range append(append(append([]string{}, rule.Terms...), rule.Exceptions...), rule.Replacements...) {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("prompt rule %s contains a blank value", rule.ID)
			}
		}
	}
	for _, rule := range file.Rules {
		if rule.TranslationOf != "" {
			if ids[rule.TranslationOf] == 0 || rule.TranslationOf == rule.ID {
				return fmt.Errorf("prompt rule %s has invalid translation_of", rule.ID)
			}
			base := file.Rules[indexOfRule(file.Rules, rule.TranslationOf)]
			if base.TranslationOf == rule.ID {
				return fmt.Errorf("prompt rule %s creates a translation cycle", rule.ID)
			}
		}
	}
	return nil
}

func indexOfRule(rules []Rule, id string) int {
	for index := range rules {
		if rules[index].ID == id {
			return index
		}
	}
	return -1
}

func (e *Engine) insert(term []rune, output termOutput) {
	current := 0
	for _, item := range term {
		next, ok := e.nodes[current].next[item]
		if !ok {
			next = len(e.nodes)
			e.nodes[current].next[item] = next
			e.nodes = append(e.nodes, node{next: map[rune]int{}})
		}
		current = next
	}
	e.nodes[current].outputs = append(e.nodes[current].outputs, output)
}

func (e *Engine) buildFailures() {
	queue := make([]int, 0, len(e.nodes))
	for _, child := range e.nodes[0].next {
		queue = append(queue, child)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for character, child := range e.nodes[current].next {
			failure := e.nodes[current].fail
			for failure != 0 {
				if target, ok := e.nodes[failure].next[character]; ok {
					failure = target
					break
				}
				failure = e.nodes[failure].fail
			}
			if failure == 0 {
				if target, ok := e.nodes[0].next[character]; ok && target != child {
					failure = target
				}
			}
			e.nodes[child].fail = failure
			e.nodes[child].outputs = append(e.nodes[child].outputs, e.nodes[failure].outputs...)
			queue = append(queue, child)
		}
	}
}

type normalizedText struct {
	runes  []rune
	starts []int
	ends   []int
}

func normalizeText(value string) normalizedText {
	result := normalizedText{}
	var lastSpace bool
	for byteStart, character := range value {
		byteEnd := byteStart + utf8.RuneLen(character)
		for _, normalized := range norm.NFKC.String(string(character)) {
			normalized = unicode.ToLower(normalized)
			if unicode.IsSpace(normalized) {
				normalized = ' '
				if lastSpace {
					result.ends[len(result.ends)-1] = byteEnd
					continue
				}
				lastSpace = true
			} else {
				lastSpace = false
			}
			result.runes = append(result.runes, normalized)
			result.starts = append(result.starts, byteStart)
			result.ends = append(result.ends, byteEnd)
		}
	}
	return result
}

func normalizeRunes(value string) []rune {
	return normalizeText(strings.TrimSpace(value)).runes
}

type candidate struct {
	ruleIndex  int
	startRune  int
	endRune    int
	startByte  int
	endByte    int
	termLength int
}

func (e *Engine) Refine(prompt string) Result {
	normalized := normalizeText(prompt)
	state := 0
	candidates := make([]candidate, 0)
	for index, character := range normalized.runes {
		for state != 0 {
			if _, ok := e.nodes[state].next[character]; ok {
				break
			}
			state = e.nodes[state].fail
		}
		if next, ok := e.nodes[state].next[character]; ok {
			state = next
		} else {
			state = 0
		}
		for _, output := range e.nodes[state].outputs {
			startRune := index - output.termRunes + 1
			if startRune < 0 || !wordBoundary(normalized.runes, startRune, index+1, e.rules[output.ruleIndex].Locale) {
				continue
			}
			rule := e.rules[output.ruleIndex]
			if exceptionOverlaps(normalized.runes, startRune, index+1, rule.Exceptions) {
				continue
			}
			candidates = append(candidates, candidate{
				ruleIndex: output.ruleIndex, startRune: startRune, endRune: index + 1,
				startByte: normalized.starts[startRune], endByte: normalized.ends[index], termLength: output.termRunes,
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].startByte != candidates[j].startByte {
			return candidates[i].startByte < candidates[j].startByte
		}
		if candidates[i].termLength != candidates[j].termLength {
			return candidates[i].termLength > candidates[j].termLength
		}
		leftPriority := sourcePriority(e.rules[candidates[i].ruleIndex].Source.Type)
		rightPriority := sourcePriority(e.rules[candidates[j].ruleIndex].Source.Type)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return e.rules[candidates[i].ruleIndex].ID < e.rules[candidates[j].ruleIndex].ID
	})
	selected := make([]candidate, 0, len(candidates))
	lastEnd := -1
	for _, item := range candidates {
		if item.startByte < lastEnd {
			continue
		}
		selected = append(selected, item)
		lastEnd = item.endByte
	}
	result := Result{
		PolicyVersion: e.version,
		Status:        "clean",
		Segments:      make([]Segment, 0),
		Findings:      make([]Finding, 0),
	}
	cursor := 0
	for _, item := range selected {
		rule := e.rules[item.ruleIndex]
		id := fmt.Sprintf("%s:%d", rule.ID, item.startByte)
		if item.startByte > cursor {
			result.Segments = append(result.Segments, Segment{Text: prompt[cursor:item.startByte]})
		}
		original := prompt[item.startByte:item.endByte]
		result.Segments = append(result.Segments, Segment{Text: original, FindingID: id})
		result.Findings = append(result.Findings, Finding{
			ID: id, RuleID: rule.ID, Locale: rule.Locale, Category: rule.Category, Mode: rule.Mode,
			Original: original, Reason: rule.Reason, Replacements: append([]string(nil), rule.Replacements...),
			Start: item.startByte, End: item.endByte,
		})
		cursor = item.endByte
	}
	if cursor < len(prompt) {
		result.Segments = append(result.Segments, Segment{Text: prompt[cursor:]})
	}
	if len(result.Findings) > 0 {
		result.Status = "findings"
	}
	return result
}

func sourcePriority(sourceType string) int {
	switch sourceType {
	case "workspace":
		return 0
	case "translation":
		return 1
	case "official":
		return 2
	case "production":
		return 3
	case "community":
		return 4
	default:
		return 5
	}
}

func wordBoundary(text []rune, start, end int, locale string) bool {
	if locale != "en" {
		return true
	}
	isWord := func(value rune) bool {
		return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_'
	}
	return (start == 0 || !isWord(text[start-1])) && (end == len(text) || !isWord(text[end]))
}

func exceptionOverlaps(text []rune, matchStart, matchEnd int, exceptions []string) bool {
	for _, exception := range exceptions {
		needle := normalizeRunes(exception)
		for start := 0; start+len(needle) <= len(text); start++ {
			if runesEqual(text[start:start+len(needle)], needle) && start < matchEnd && start+len(needle) > matchStart {
				return true
			}
		}
	}
	return false
}

func runesEqual(left, right []rune) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
