// Package lang tells Polish questions from questions in other languages.
//
// It is a small heuristic, not a general language detector: the knowledge
// base answers in Polish only, so all it has to do is reject text that is
// clearly in another language. Ambiguous text (a product name, a short
// technical phrase) counts as Polish.
package lang

import (
	"regexp"
	"strings"
	"unicode"
)

var word = regexp.MustCompile(`\p{L}+`)

const polishLetters = "ąćęłńóśźż"

func set(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// Words shared by Polish and English ("i", "a", "to", "do", "on") are left out.
var (
	polish = set(`się sie nie jak jest są czy co mi mnie mój moja moje mam ma mogę można trzeba
		dlaczego gdzie kiedy który która które jaki jaka jakie ten ta te tak już oraz lub ale
		po przy od za dla ze we w z na o pod nad bez przez u czemu działa nie działa jeśli
		komputer komputera drukarka hasło konto przeglądarka`)
	other = set(`the is are was my how what why where when can cannot does doesn not and with of in
		for it this that you your have has from will won there please help
		der die das und ist nicht wie ich mein le la les est pas comment je mon el los es no como mi`)
)

// IsPolish reports whether text should be treated as a Polish question.
func IsPolish(text string) bool {
	var letters, latin int
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if unicode.Is(unicode.Latin, r) {
				latin++
			}
		}
	}
	if letters == 0 {
		return false
	}
	if latin*10 < letters*7 { // mostly Cyrillic, Greek, CJK, ...
		return false
	}
	lower := strings.ToLower(text)
	if strings.ContainsAny(lower, polishLetters) {
		return true
	}
	var pl, ot int
	for _, w := range word.FindAllString(lower, -1) {
		if polish[w] {
			pl++
		}
		if other[w] {
			ot++
		}
	}
	return ot <= pl
}
