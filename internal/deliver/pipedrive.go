package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PipedriveClient struct {
	BaseURL   string // default https://api.pipedrive.com
	Token     string
	StageID   int64
	FieldKeys map[string]string // nip, regon, krs, pkd, board_members, source
	HTTP      *http.Client
}

type PipedriveLead struct {
	Company     string
	NIP         string
	REGON       string
	KRS         string
	PKD         string
	Address     string
	Website     string
	Board       []string
	Positions   []string
	NoteContent string
}

type PushResult struct {
	OrgID       int64
	DealID      int64
	OrgCreated  bool
	DealCreated bool
}

func (c *PipedriveClient) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.pipedrive.com"
}

func (c *PipedriveClient) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *PipedriveClient) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	if query == nil {
		query = url.Values{}
	}
	query.Set("api_token", c.Token)
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path+"?"+query.Encode(), rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("pipedrive %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pipedrive %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *PipedriveClient) FindOrgByNIP(ctx context.Context, nip string) (int64, bool, error) {
	var out struct {
		Data struct {
			Items []struct {
				Item struct {
					ID int64 `json:"id"`
				} `json:"item"`
			} `json:"items"`
		} `json:"data"`
	}
	q := url.Values{"term": {nip}, "fields": {"custom_fields"}, "exact_match": {"true"}}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/search", q, nil, &out); err != nil {
		return 0, false, err
	}
	if len(out.Data.Items) == 0 {
		return 0, false, nil
	}
	return out.Data.Items[0].Item.ID, true, nil
}

func (c *PipedriveClient) CreateOrg(ctx context.Context, l PipedriveLead) (int64, error) {
	body := map[string]any{"name": l.Company, "address": l.Address}
	set := func(field, val string) {
		if key := c.FieldKeys[field]; key != "" && val != "" {
			body[key] = val
		}
	}
	set("nip", l.NIP)
	set("regon", l.REGON)
	set("krs", l.KRS)
	set("pkd", l.PKD)
	set("board_members", strings.Join(l.Board, ", "))
	set("source", "lead-engine")
	var out struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/organizations", nil, body, &out); err != nil {
		return 0, err
	}
	return out.Data.ID, nil
}

func (c *PipedriveClient) OpenDealID(ctx context.Context, orgID int64) (int64, bool, error) {
	var out struct {
		Data []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	q := url.Values{"org_id": {fmt.Sprint(orgID)}, "status": {"open"}}
	if err := c.do(ctx, http.MethodGet, "/v1/deals", q, nil, &out); err != nil {
		return 0, false, err
	}
	if len(out.Data) == 0 {
		return 0, false, nil
	}
	return out.Data[0].ID, true, nil
}

func (c *PipedriveClient) CreateDeal(ctx context.Context, orgID int64, title string) (int64, error) {
	body := map[string]any{"title": title, "org_id": orgID}
	if c.StageID != 0 {
		body["stage_id"] = c.StageID
	}
	var out struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/deals", nil, body, &out); err != nil {
		return 0, err
	}
	return out.Data.ID, nil
}

func (c *PipedriveClient) AddNote(ctx context.Context, dealID, orgID int64, content string) error {
	body := map[string]any{"content": content}
	if dealID != 0 {
		body["deal_id"] = dealID
	} else {
		body["org_id"] = orgID
	}
	var out struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	return c.do(ctx, http.MethodPost, "/v1/notes", nil, body, &out)
}

// PushLead applies the spec's duplicate policy: never duplicate orgs; new
// deal only when no open deal exists; otherwise note the open deal.
func (c *PipedriveClient) PushLead(ctx context.Context, l PipedriveLead) (PushResult, error) {
	var res PushResult
	orgID, found, err := c.FindOrgByNIP(ctx, l.NIP)
	if err != nil {
		return res, err
	}
	if !found {
		orgID, err = c.CreateOrg(ctx, l)
		if err != nil {
			return res, err
		}
		res.OrgCreated = true
	}
	res.OrgID = orgID

	title := strings.Join(l.Positions, ", ") + " — " + l.Company
	if found {
		if dealID, open, err := c.OpenDealID(ctx, orgID); err != nil {
			return res, err
		} else if open {
			res.DealID = dealID
			return res, c.AddNote(ctx, dealID, orgID, l.NoteContent)
		}
	}
	dealID, err := c.CreateDeal(ctx, orgID, title)
	if err != nil {
		return res, err
	}
	res.DealID = dealID
	res.DealCreated = true
	return res, c.AddNote(ctx, dealID, orgID, l.NoteContent)
}

// EnsureOrgFields creates the custom Organization fields lead-engine needs
// and returns name -> field key. Used by `lead-engine pipedrive setup`;
// run once, then persist the keys in config [pipedrive.field_keys].
// Idempotent: existing fields (matched by label) are reused, missing ones created.
func (c *PipedriveClient) EnsureOrgFields(ctx context.Context) (map[string]string, error) {
	wanted := map[string]string{ // config name -> Pipedrive field label
		"nip": "NIP", "regon": "REGON", "krs": "KRS",
		"pkd": "PKD", "board_members": "Zarząd", "source": "Lead Source",
	}
	var existing struct {
		Data []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizationFields", nil, nil, &existing); err != nil {
		return nil, fmt.Errorf("list fields: %w", err)
	}
	byLabel := map[string]string{}
	for _, f := range existing.Data {
		byLabel[f.Name] = f.Key
	}
	keys := map[string]string{}
	for name, label := range wanted {
		if key, ok := byLabel[label]; ok {
			keys[name] = key
			continue
		}
		body := map[string]any{"name": label, "field_type": "varchar"}
		var out struct {
			Data struct {
				Key string `json:"key"`
			} `json:"data"`
		}
		if err := c.do(ctx, http.MethodPost, "/v1/organizationFields", nil, body, &out); err != nil {
			return keys, fmt.Errorf("create field %s: %w", label, err)
		}
		keys[name] = out.Data.Key
	}
	return keys, nil
}
