package deliver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakePipedrive implements just enough of the v1 API:
// org search (miss then hit), org create, deal list/create, note create,
// organizationFields list/create.
type fakePipedrive struct {
	orgs   map[string]int64 // nip -> org id
	deals  map[int64]int64  // org id -> open deal id
	notes  []string
	fields []struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	next int64
}

func (f *fakePipedrive) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/organizations/search":
			nip := r.URL.Query().Get("term")
			if id, ok := f.orgs[nip]; ok {
				fmt.Fprintf(w, `{"success":true,"data":{"items":[{"item":{"id":%d}}]}}`, id)
			} else {
				fmt.Fprint(w, `{"success":true,"data":{"items":[]}}`)
			}
		case r.URL.Path == "/v1/organizations" && r.Method == "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.next++
			f.orgs[body["nip_key"].(string)] = f.next
			fmt.Fprintf(w, `{"success":true,"data":{"id":%d}}`, f.next)
		case r.URL.Path == "/v1/deals" && r.Method == "GET":
			// org_id query; return open deal if present
			var orgID int64
			fmt.Sscanf(r.URL.Query().Get("org_id"), "%d", &orgID)
			if id, ok := f.deals[orgID]; ok {
				fmt.Fprintf(w, `{"success":true,"data":[{"id":%d,"status":"open"}]}`, id)
			} else {
				fmt.Fprint(w, `{"success":true,"data":[]}`)
			}
		case r.URL.Path == "/v1/deals" && r.Method == "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.next++
			f.deals[int64(body["org_id"].(float64))] = f.next
			fmt.Fprintf(w, `{"success":true,"data":{"id":%d}}`, f.next)
		case r.URL.Path == "/v1/notes" && r.Method == "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.notes = append(f.notes, body["content"].(string))
			fmt.Fprint(w, `{"success":true,"data":{"id":1}}`)
		case r.URL.Path == "/v1/organizationFields" && r.Method == "GET":
			data, _ := json.Marshal(f.fields)
			fmt.Fprintf(w, `{"success":true,"data":%s}`, data)
		case r.URL.Path == "/v1/organizationFields" && r.Method == "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.next++
			key := fmt.Sprintf("key%d", f.next)
			name := body["name"].(string)
			f.fields = append(f.fields, struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			}{Key: key, Name: name})
			fmt.Fprintf(w, `{"success":true,"data":{"key":%q}}`, key)
		default:
			http.NotFound(w, r)
		}
	}
}

func newTestPD(srvURL string) *PipedriveClient {
	return &PipedriveClient{
		BaseURL: srvURL, Token: "tok",
		FieldKeys: map[string]string{"nip": "nip_key", "regon": "regon_key",
			"krs": "krs_key", "pkd": "pkd_key", "board_members": "board_key", "source": "source_key"},
	}
}

func TestPushCreatesOrgAndDeal(t *testing.T) {
	f := &fakePipedrive{orgs: map[string]int64{}, deals: map[int64]int64{}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	pd := newTestPD(srv.URL)
	res, err := pd.PushLead(context.Background(), PipedriveLead{
		Company: "Stalmet Sp. z o.o.", NIP: "1234567890",
		Positions: []string{"Operator CNC"}, NoteContent: "details...",
	})
	if err != nil {
		t.Fatalf("PushLead: %v", err)
	}
	if res.OrgID == 0 || res.DealID == 0 || !res.OrgCreated {
		t.Errorf("res = %+v", res)
	}
}

func TestPushExistingOrgOpenDealAddsNote(t *testing.T) {
	f := &fakePipedrive{orgs: map[string]int64{"1234567890": 7}, deals: map[int64]int64{7: 42}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	pd := newTestPD(srv.URL)
	res, err := pd.PushLead(context.Background(), PipedriveLead{
		Company: "Stalmet", NIP: "1234567890",
		Positions: []string{"Monter"}, NoteContent: "hiring again",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OrgCreated || res.DealCreated || res.DealID != 42 {
		t.Errorf("res = %+v", res)
	}
	if len(f.notes) != 1 {
		t.Errorf("notes = %v", f.notes)
	}
}

func TestEnsureOrgFieldsIdempotent(t *testing.T) {
	f := &fakePipedrive{orgs: map[string]int64{}, deals: map[int64]int64{}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	pd := newTestPD(srv.URL)

	k1, err := pd.EnsureOrgFields(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	k2, err := pd.EnsureOrgFields(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.fields) != 6 {
		t.Errorf("fields created = %d, want 6 (second run must reuse)", len(f.fields))
	}
	if len(k1) != 6 || len(k2) != 6 {
		t.Errorf("keys: %v / %v", k1, k2)
	}
	for name := range k1 {
		if k1[name] != k2[name] {
			t.Errorf("key for %s changed between runs: %s -> %s", name, k1[name], k2[name])
		}
	}
}
