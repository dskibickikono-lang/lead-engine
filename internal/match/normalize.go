package match

import "strings"

var polishFold = strings.NewReplacer(
	"ą", "a", "ć", "c", "ę", "e", "ł", "l", "ń", "n",
	"ó", "o", "ś", "s", "ź", "z", "ż", "z",
)

// Longest suffixes first so "spolka z ograniczona odpowiedzialnoscia"
// is removed before "spolka".
var legalSuffixes = []string{
	"spolka z ograniczona odpowiedzialnoscia spolka komandytowa",
	"spolka z ograniczona odpowiedzialnoscia",
	"spolka komandytowo akcyjna",
	"spolka komandytowa",
	"spolka akcyjna",
	"spolka jawna",
	"spolka cywilna",
	"sp z o o sp k",
	"sp z o o",
	"sp k",
	"sp j",
	"s c",
	"s a",
	"z o o",
}

func Normalize(name string) string {
	s := strings.ToLower(name)
	s = polishFold.Replace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	s = strings.Join(strings.Fields(b.String()), " ")
	for changed := true; changed; {
		changed = false
		for _, suf := range legalSuffixes {
			if strings.HasSuffix(s, " "+suf) || s == suf {
				s = strings.TrimSpace(strings.TrimSuffix(s, suf))
				changed = true
			}
		}
	}
	return s
}
