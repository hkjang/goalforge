package sqlite

import (
	"fmt"
	"strconv"
	"strings"
)

// Expectation is a completion criterion's target, parsed into the three things
// a judgement needs: which direction satisfies it, what number, and in what
// unit.
//
// The engine used to judge every numeric criterion with ">=". That is right for
// a throughput floor and exactly backwards for a latency ceiling: measuring
// 5000ms against a 200ms target was reported as met, so the goal whose whole
// point was to be fast reported complete at its worst. Direction is not a
// detail of the number; it is half of what the number means.
type Expectation struct {
	// Comparator is one of >=, <=, >, <, =, !=. It is always set for a numeric
	// expectation.
	Comparator string
	Value      float64
	Unit       string
	// Numeric is false for expectations like "true" or a commit SHA, which are
	// settled by equality and nothing else.
	Numeric bool
	Raw     string
}

// comparatorWords maps every spelling of a direction onto one comparator, so a
// person writing a goal does not have to learn which form the parser happens to
// accept. The symbolic forms are listed longest-first because "<=" must be
// recognised before "<".
var comparatorPrefixes = []struct{ prefix, comparator string }{
	{"<=", "<="}, {">=", ">="}, {"!=", "!="}, {"==", "="},
	{"≤", "<="}, {"≥", ">="}, {"≠", "!="},
	{"<", "<"}, {">", ">"}, {"=", "="},
}

var comparatorWords = map[string]string{
	"max": "<=", "at_most": "<=", "atmost": "<=", "under": "<=", "최대": "<=", "이하": "<=",
	"min": ">=", "at_least": ">=", "atleast": ">=", "over": ">=", "최소": ">=", "이상": ">=",
	"exactly": "=", "eq": "=", "정확히": "=",
	"not": "!=", "ne": "!=",
}

// unitFamilies converts a unit to a family and a multiplier into that family's
// base unit. Comparing across families is refused rather than guessed: a judge
// that silently compares 200ms against 150MB settles a latency target with a
// memory measurement.
var unitFamilies = map[string]struct {
	family string
	factor float64
}{
	"ns": {"time", 1e-6}, "us": {"time", 1e-3}, "µs": {"time", 1e-3},
	"ms": {"time", 1}, "s": {"time", 1000}, "sec": {"time", 1000},
	"m": {"time", 60000}, "min": {"time", 60000}, "h": {"time", 3600000},
	"b": {"bytes", 1}, "kb": {"bytes", 1024}, "mb": {"bytes", 1024 * 1024},
	"gb": {"bytes", 1024 * 1024 * 1024}, "tb": {"bytes", 1024 * 1024 * 1024 * 1024},
	"%": {"percent", 1}, "percent": {"percent", 1},
}

// ParseExpectation reads a criterion's expected value.
//
// A bare number keeps meaning "at least". Goals already recorded were written
// under that rule, and silently flipping them would change the verdict on work
// already judged complete.
func ParseExpectation(expected string) Expectation {
	raw := strings.TrimSpace(expected)
	rest := raw
	comparator := ""
	for _, candidate := range comparatorPrefixes {
		if strings.HasPrefix(rest, candidate.prefix) {
			comparator = candidate.comparator
			rest = strings.TrimSpace(strings.TrimPrefix(rest, candidate.prefix))
			break
		}
	}
	if comparator == "" {
		if word, remainder, ok := leadingWord(rest); ok {
			if mapped, known := comparatorWords[strings.ToLower(word)]; known {
				comparator, rest = mapped, remainder
			}
		}
	}
	// A trailing Korean postposition — "200ms 이하" — reads the same as a
	// leading one and is how the requirement is usually written.
	if comparator == "" {
		for suffix, mapped := range map[string]string{"이하": "<=", "이상": ">=", "미만": "<", "초과": ">"} {
			if strings.HasSuffix(rest, suffix) {
				comparator = mapped
				rest = strings.TrimSpace(strings.TrimSuffix(rest, suffix))
				break
			}
		}
	}
	value, unit, ok := splitValueAndUnit(rest)
	if !ok {
		return Expectation{Raw: raw}
	}
	if comparator == "" {
		comparator = ">="
	}
	return Expectation{Comparator: comparator, Value: value, Unit: unit, Numeric: true, Raw: raw}
}

func leadingWord(text string) (string, string, bool) {
	index := strings.IndexAny(text, " \t")
	if index <= 0 {
		return "", text, false
	}
	return text[:index], strings.TrimSpace(text[index:]), true
}

// splitValueAndUnit reads "200ms" and "99.9 %" alike.
func splitValueAndUnit(text string) (float64, string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, "", false
	}
	end := 0
	for end < len(text) {
		c := text[end]
		if (c >= '0' && c <= '9') || c == '.' || (end == 0 && (c == '-' || c == '+')) {
			end++
			continue
		}
		break
	}
	value, err := strconv.ParseFloat(text[:end], 64)
	if err != nil {
		return 0, "", false
	}
	return value, strings.ToLower(strings.TrimSpace(text[end:])), true
}

// normalizeUnit converts a value into its family's base unit, and reports the
// family so two sides can be checked for comparability.
func normalizeUnit(value float64, unit string) (float64, string) {
	if unit == "" {
		return value, ""
	}
	conversion, known := unitFamilies[unit]
	if !known {
		// An unrecognised unit is still a unit: "rps" compares with "rps" and
		// not with anything else.
		return value, "?" + unit
	}
	return value * conversion.factor, conversion.family
}

// Describe says what the expectation asks for, in the direction it asks for it,
// so a report reads "200ms 이하" rather than leaving the reader to guess.
func (e Expectation) Describe() string {
	if !e.Numeric {
		return e.Raw
	}
	number := strconv.FormatFloat(e.Value, 'f', -1, 64) + e.Unit
	switch e.Comparator {
	case ">=":
		return number + " 이상"
	case "<=":
		return number + " 이하"
	case ">":
		return number + " 초과"
	case "<":
		return number + " 미만"
	case "!=":
		return number + " 이 아님"
	}
	return number
}

// Satisfied judges an actual value, and says why when it does not.
func (e Expectation) Satisfied(actual string) (bool, string) {
	actual = strings.TrimSpace(actual)
	if !e.Numeric {
		if e.Raw == actual {
			return true, ""
		}
		return false, fmt.Sprintf("기대 %q, 실제 %q", e.Raw, actual)
	}
	actualValue, actualUnit, ok := splitValueAndUnit(actual)
	if !ok {
		if e.Raw == actual {
			return true, ""
		}
		return false, fmt.Sprintf("%s 를 기대했는데 측정값이 수치가 아닙니다: %q", e.Describe(), actual)
	}
	left, leftFamily := normalizeUnit(actualValue, actualUnit)
	right, rightFamily := normalizeUnit(e.Value, e.Unit)
	if leftFamily != rightFamily && leftFamily != "" && rightFamily != "" {
		return false, fmt.Sprintf("단위가 다릅니다: 기대 %s, 측정 %s — 같은 단위로 측정해야 판정할 수 있습니다",
			e.Describe(), actual)
	}
	if compare(e.Comparator, left, right) {
		return true, ""
	}
	return false, fmt.Sprintf("%s 를 기대했는데 %s 입니다", e.Describe(), actual)
}

func compare(comparator string, left, right float64) bool {
	switch comparator {
	case ">=":
		return left >= right
	case "<=":
		return left <= right
	case ">":
		return left > right
	case "<":
		return left < right
	case "=":
		return left == right
	case "!=":
		return left != right
	}
	return false
}

// JudgeCriterion answers whether a measurement satisfies an expectation, and
// gives the reason when it does not. The reason is what turns "미충족" on a
// report into something the reader can act on.
func JudgeCriterion(expected, actual string) (bool, string) {
	if expected == actual {
		return true, ""
	}
	return ParseExpectation(expected).Satisfied(actual)
}
