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

// FetchBoard returns the management board for a KRS number, or (nil, nil)
// when the registry has no such entity (404) — non-blocking by design.
func (c *Client) FetchBoard(ctx context.Context, krsNum string) ([]BoardMember, error) {
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
	var board []BoardMember
	for _, m := range o.Odpis.Dane.Dzial2.Reprezentacja.Sklad {
		name := strings.TrimSpace(m.Imiona.Imie + " " + m.Nazwisko.NazwiskoICzlon)
		if name == "" {
			continue
		}
		board = append(board, BoardMember{Name: name, Role: m.Funkcja})
	}
	return board, nil
}
