// Package regon implements a minimal BIR1.1 client: NIP -> contact data,
// address, and KRS number. Free API; sessions are opened and closed per
// lookup batch via Login/Logout.
package regon

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const DefaultEndpoint = "https://wyszukiwarkaregon.stat.gov.pl/wsBIR/UslugaBIRzewnPubl.svc"

type Report struct {
	REGON   string
	Type    string // P = legal entity, F = natural person
	KRS     string
	Phone   string
	Email   string
	Website string
	Address string
}

// ErrNotFound means BIR has no entity for the query (ErrorCode 4) — a
// definitive answer, safe to cache, distinct from transport/session errors.
var ErrNotFound = errors.New("regon: not found")

// errRow surfaces BIR's in-band error rows (ErrorCode/ErrorMessagePl).
func errRow(row map[string]string) error {
	code, ok := row["ErrorCode"]
	if !ok {
		return nil
	}
	if code == "4" {
		return ErrNotFound
	}
	return fmt.Errorf("regon: ErrorCode %s: %s", code, row["ErrorMessagePl"])
}

type Client struct {
	Endpoint string
	APIKey   string
	HTTP     *http.Client
}

func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return DefaultEndpoint
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) call(ctx context.Context, sid, envelope string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), strings.NewReader(envelope))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	if sid != "" {
		req.Header.Set("sid", sid)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("regon: status %d", resp.StatusCode)
	}
	// Responses arrive as multipart MTOM; the envelope is the part between
	// the first <s:Envelope and its closing tag.
	s := string(body)
	start := strings.Index(s, "<s:Envelope")
	if start < 0 {
		start = strings.Index(s, "<soap:Envelope")
	}
	if start < 0 {
		return "", fmt.Errorf("regon: no envelope in response")
	}
	end := strings.Index(s[start:], "Envelope>")
	if end < 0 {
		return "", fmt.Errorf("regon: truncated envelope")
	}
	return s[start : start+end+len("Envelope>")], nil
}

// resultOf extracts the inner text of <XxxResult> and HTML-unescapes it
// (BIR returns embedded XML escaped inside the result element).
func resultOf(envelope, result string) string {
	re := regexp.MustCompile(`(?s)<` + result + `[^>]*>(.*?)</` + result + `>`)
	m := re.FindStringSubmatch(envelope)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

// parseDane decodes BIR's <root><dane>...</dane></root> rows into maps.
func parseDane(innerXML string) ([]map[string]string, error) {
	dec := xml.NewDecoder(strings.NewReader(innerXML))
	var rows []map[string]string
	var cur map[string]string
	var field string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "dane" {
				cur = map[string]string{}
			} else if cur != nil {
				field = t.Name.Local
			}
		case xml.CharData:
			if cur != nil && field != "" {
				cur[field] += string(t)
			}
		case xml.EndElement:
			if t.Name.Local == "dane" && cur != nil {
				rows = append(rows, cur)
				cur = nil
			}
			field = ""
		}
	}
	return rows, nil
}

func (c *Client) LookupByNIP(ctx context.Context, nip string) (*Report, error) {
	env, err := c.call(ctx, "", fmt.Sprintf(envZaloguj, c.endpoint(), c.APIKey))
	if err != nil {
		return nil, fmt.Errorf("regon login: %w", err)
	}
	sid := strings.TrimSpace(resultOf(env, "ZalogujResult"))
	if sid == "" {
		return nil, fmt.Errorf("regon login: empty session id (bad api key?)")
	}
	defer func() {
		// Best-effort logout with a fresh context: the parent ctx may already
		// be cancelled, and GUS enforces a per-key session limit.
		lctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.call(lctx, sid, fmt.Sprintf(envWyloguj, c.endpoint(), sid)) //nolint:errcheck
	}()

	env, err = c.call(ctx, sid, fmt.Sprintf(envSzukaj, c.endpoint(), nip))
	if err != nil {
		return nil, fmt.Errorf("regon search %s: %w", nip, err)
	}
	rows, err := parseDane(resultOf(env, "DaneSzukajPodmiotyResult"))
	if err != nil || len(rows) == 0 {
		return nil, fmt.Errorf("regon search %s: no result (err=%v)", nip, err)
	}
	if err := errRow(rows[0]); err != nil {
		return nil, fmt.Errorf("regon search %s: %w", nip, err)
	}
	rep := &Report{REGON: rows[0]["Regon"], Type: rows[0]["Typ"]}
	if rep.Type != "P" {
		return rep, nil // sole traders: no praw_ report, no KRS
	}

	env, err = c.call(ctx, sid, fmt.Sprintf(envRaport, c.endpoint(), rep.REGON, "BIR11OsPrawna"))
	if err != nil {
		return rep, fmt.Errorf("regon report %s: %w", rep.REGON, err)
	}
	rrows, err := parseDane(resultOf(env, "DanePobierzPelnyRaportResult"))
	if err != nil || len(rrows) == 0 {
		return rep, nil // search data is still useful
	}
	if err := errRow(rrows[0]); err != nil {
		return rep, fmt.Errorf("regon report %s: %w", rep.REGON, err)
	}
	d := rrows[0]
	rep.Phone = d["praw_numerTelefonu"]
	rep.Email = d["praw_adresEmail"]
	rep.Website = d["praw_adresStronyinternetowej"]
	rep.KRS = d["praw_numerWRejestrzeEwidencji"]
	street := strings.TrimSpace(d["praw_adSiedzUlica_Nazwa"] + " " + d["praw_adSiedzNumerNieruchomosci"])
	zip := d["praw_adSiedzKodPocztowy"]
	if len(zip) == 5 {
		zip = zip[:2] + "-" + zip[2:]
	}
	cityPart := strings.TrimSpace(zip + " " + d["praw_adSiedzMiejscowosc_Nazwa"])
	parts := []string{}
	if street != "" {
		parts = append(parts, street)
	}
	if cityPart != "" {
		parts = append(parts, cityPart)
	}
	rep.Address = strings.Join(parts, ", ")
	return rep, nil
}
