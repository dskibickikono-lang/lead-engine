package match

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Stalmet Sp. z o.o.":          "stalmet",
		"STALMET spółka z ograniczoną odpowiedzialnością": "stalmet",
		"Żółć  S.A.":                  "zolc",
		"Kowalski sp.j.":              "kowalski",
		"ABC-Produkcja Sp. K.":        "abc produkcja",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
