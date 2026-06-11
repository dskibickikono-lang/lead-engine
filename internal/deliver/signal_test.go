package deliver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSignalSend(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/send" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		w.WriteHeader(201)
	}))
	defer srv.Close()

	c := &SignalClient{APIURL: srv.URL, Number: "+48111222333", Recipients: []string{"group.abc"}}
	if err := c.Send(context.Background(), "hello team"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(got) != 1 || got[0]["message"] != "hello team" || got[0]["number"] != "+48111222333" {
		t.Errorf("payload = %+v", got)
	}

	// Long messages split into <=4000-char parts.
	got = nil
	if err := c.Send(context.Background(), strings.Repeat("x", 9000)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("parts = %d, want 3", len(got))
	}

	// Polish (multi-byte) text: no part may end mid-rune.
	got = nil
	if err := c.Send(context.Background(), "a"+strings.Repeat("ł", 4500)); err != nil {
		t.Fatal(err)
	}
	for i, m := range got {
		if !utf8.ValidString(m["message"].(string)) {
			t.Errorf("part %d is not valid UTF-8", i)
		}
	}
}

func TestSignalRetriesThenFails(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()
	c := &SignalClient{APIURL: srv.URL, Number: "+48111222333",
		Recipients: []string{"g"}, Backoff: time.Millisecond}
	if err := c.Send(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}
