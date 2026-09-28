package policy

import "unicode"

// Korean particles agree with the final sound of the word before them: 승인은
// but 반려는, 병합을 but 게시를. Picking them by hand goes wrong the moment an
// operation name is added — this package's own refusal message read "승인 는"
// — and a product that writes its own language that way reads like a machine
// filled in a form.
//
// Topic appends 은/는, Object appends 을/를.
func Topic(word string) string { return word + particle(word, "은", "는") }

// Object appends the Korean object particle matching the word's final sound.
func Object(word string) string { return word + particle(word, "을", "를") }

func particle(word, afterConsonant, afterVowel string) string {
	if word == "" {
		return afterVowel
	}
	final := lastMeaningfulRune(word)
	if hasFinalConsonant(final) {
		return afterConsonant
	}
	return afterVowel
}

// lastMeaningfulRune skips trailing punctuation and spaces, so "merge-branch)"
// still agrees with the sound of "branch".
func lastMeaningfulRune(word string) rune {
	runes := []rune(word)
	for i := len(runes) - 1; i >= 0; i-- {
		r := runes[i]
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
	}
	return 0
}

// hasFinalConsonant reports whether a syllable ends in a consonant, which is
// what the particle agrees with.
func hasFinalConsonant(r rune) bool {
	// Hangul syllables are laid out so the final consonant is recoverable
	// arithmetically: 28 finals per vowel, index 0 meaning "none".
	if r >= 0xAC00 && r <= 0xD7A3 {
		return (r-0xAC00)%28 != 0
	}
	// Digits agree with how they are read aloud in Korean.
	switch r {
	case '0', '1', '3', '6', '7', '8':
		return true
	case '2', '4', '5', '9':
		return false
	}
	// For Latin words the reading varies; the consonant form is the safer
	// default because it is the one that stays grammatical when read as a
	// borrowed noun.
	return r != 0
}
