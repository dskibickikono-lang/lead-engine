// Package krs fetches current KRS extracts from the free MS registry API
// (api-krs.ms.gov.pl) — the only source of board members in the pipeline.
package krs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api-krs.ms.gov.pl"

type BoardMember struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// Profile is the subset of a KRS extract the pipeline uses: the management
// board and the share capital (kapitał zakładowy), both read from a single
// OdpisAktualny fetch. ShareCapital is "" for entities without one (e.g.
// foundations, some partnerships).
type Profile struct {
	Board        []BoardMember `json:"board"`
	ShareCapital string        `json:"shareCapital"` // e.g. "5000000,00 PLN"
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

type odpisResponse struct {
	Odpis struct {
		Dane struct {
			Dzial1 struct {
				// Kapitał zakładowy lives in dział 1. Path verified against a
				// live OdpisAktualny response before relying on the value; when
				// absent the field stays empty and only the board is used.
				Kapital struct {
					WysokoscKapitaluZakladowego struct {
						Wartosc string `json:"wartosc"`
						Waluta  string `json:"waluta"`
					} `json:"wysokoscKapitaluZakladowego"`
				} `json:"kapital"`
			} `json:"dzial1"`
			Dzial2 struct {
				Reprezentacja struct {
					Sklad []struct {
						Nazwisko struct {
							NazwiskoICzlon string `json:"nazwiskoICzlon"`
						} `json:"nazwisko"`
						Imiona struct {
							Imie string `json:"imie"`
						} `json:"imiona"`
						Funkcja string `json:"funkcjaWOrganie"`
					} `json:"sklad"`
				} `json:"reprezentacja"`
			} `json:"dzial2"`
		} `json:"dane"`
	} `json:"odpis"`
}

// FetchProfile returns the board + share capital for a KRS number, or
// (nil, nil) when the registry has no such entity (404) — non-blocking by
// design. Both fields come from a single OdpisAktualny fetch.
func (c *Client) FetchProfile(ctx context.Context, krsNum string) (*Profile, error) {
	url := fmt.Sprintf("%s/api/krs/OdpisAktualny/%s?rejestr=P&format=json", c.base(), krsNum)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("krs %s: %w", krsNum, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("krs %s: status %d", krsNum, resp.StatusCode)
	}
	var o odpisResponse
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		return nil, fmt.Errorf("krs %s: decode: %w", krsNum, err)
	}
	p := &Profile{}
	for _, m := range o.Odpis.Dane.Dzial2.Reprezentacja.Sklad {
		name := strings.TrimSpace(m.Imiona.Imie + " " + m.Nazwisko.NazwiskoICzlon)
		if name == "" {
			continue
		}
		p.Board = append(p.Board, BoardMember{Name: name, Role: m.Funkcja})
	}
	if k := o.Odpis.Dane.Dzial1.Kapital.WysokoscKapitaluZakladowego; strings.TrimSpace(k.Wartosc) != "" {
		p.ShareCapital = strings.TrimSpace(k.Wartosc + " " + k.Waluta)
	}
	return p, nil
}

// FetchBoard returns just the management board for a KRS number. Retained for
// callers/tests that only need the board; delegates to FetchProfile.
func (c *Client) FetchBoard(ctx context.Context, krsNum string) ([]BoardMember, error) {
	p, err := c.FetchProfile(ctx, krsNum)
	if err != nil || p == nil {
		return nil, err
	}
	return p.Board, nil
}
