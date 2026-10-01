package standards

import (
	"fmt"
	"sort"
)

// Shipped is every catalogue this build carries.
//
// A registry rather than one function, because a project's shape decides which
// catalogue describes it. Holding a command-line tool to a web service's
// criteria reports a dozen gaps it does not have, and the operator learns to
// read past the report.
func Shipped() []Pack {
	return []Pack{GoReactOfflineService(), GoCommandLineTool()}
}

// ByRef resolves a pinned "id@version".
//
// A reference this build does not carry is refused rather than silently
// answered with whatever is compiled in. A project pinned to a catalogue
// nobody has would otherwise be measured against a different one and told it
// was the one it chose.
func ByRef(ref string) (Pack, error) {
	packs := Shipped()
	if ref == "" {
		return packs[0], nil
	}
	for _, pack := range packs {
		if pack.Ref() == ref {
			return pack, nil
		}
	}
	available := make([]string, 0, len(packs))
	for _, pack := range packs {
		available = append(available, pack.Ref())
	}
	sort.Strings(available)
	return Pack{}, fmt.Errorf("%q 팩을 알지 못합니다 — 이 빌드가 가진 것은 %v 입니다", ref, available)
}

// Suggest picks the catalogue that best fits a project's declared attributes,
// and reports whether anything actually distinguished them.
//
// Only conditional criteria count. A criterion with no condition is in the
// catalogue for every project, so counting them makes the catalogue with more
// of them win regardless of fit — the suggestion would be about size.
//
// When nothing matches, there is no suggestion. A project that declared
// nothing has said nothing to suggest from, and a catalogue chosen for someone
// on no evidence is one they cannot reconstruct the reason for.
func Suggest(attributes map[string]string) (Pack, bool) {
	best, bestScore := Pack{}, 0
	for _, pack := range Shipped() {
		score := 0
		for _, standard := range pack.Standards {
			if len(standard.AppliesWhen) > 0 && standard.AppliesTo(attributes) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = pack, score
		}
	}
	return best, bestScore > 0
}
