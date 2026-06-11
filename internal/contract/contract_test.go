package contract

import (
	"os"
	"testing"
)

func TestParseFixtures(t *testing.T) {
	for _, name := range []string{"testdata/raw-leads-cbop.json", "testdata/raw-leads-olx.json"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.Offers) != 1 {
			t.Errorf("%s: offers = %d", name, len(f.Offers))
		}
	}
}

func TestParseRejectsBadVersionAndSource(t *testing.T) {
	if _, err := Parse([]byte(`{"contractVersion":2,"source":"cbop","offers":[]}`)); err == nil {
		t.Error("version 2 accepted")
	}
	if _, err := Parse([]byte(`{"contractVersion":1,"source":"linkedin","offers":[]}`)); err == nil {
		t.Error("unknown source accepted")
	}
}

func TestNullFieldsParse(t *testing.T) {
	data, _ := os.ReadFile("testdata/raw-leads-olx.json")
	f, _ := Parse(data)
	o := f.Offers[0]
	if o.NIP != "" || o.Score != nil || o.SalaryFrom != nil {
		t.Errorf("null handling: nip=%q score=%v salaryFrom=%v", o.NIP, o.Score, o.SalaryFrom)
	}
}
