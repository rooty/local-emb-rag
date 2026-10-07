package lang

import "testing"

func TestIsPolish(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"Chrome zawiesza się i zamyka", true},
		{"chrome sie zawiesza", true},
		{"Nie mogę zalogować się do poczty", true},
		{"jak zmienic haslo do konta", true},
		{"Outlook", true}, // ambiguous: treated as Polish
		{"chrome crash", true},
		{"Chrome blocks a needed popup window", false},
		{"Outlook stuck at Processing", false},
		{"Excel nie uruchamia sie na Windows", true},
		{"Outlook pokazuje Working Offline i nie wysyla wiadomosci", true},
		{"Chrome freezes and crashes", false},
		{"How do I reset my password?", false},
		{"Хром постоянно зависает", false},
		{"Wie ändere ich mein Passwort", false},
		{"", false},
		{"12345 ???", false},
	}
	for _, c := range cases {
		if got := IsPolish(c.text); got != c.want {
			t.Errorf("IsPolish(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
