package krs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestFetchBoard(t *testing.T) {
	fixture, err := os.ReadFile("testdata/odpis.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/krs/OdpisAktualny/0000123456" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("rejestr") != "P" {
			http.Error(w, "missing rejestr", 400)
			return
		}
		w.Write(fixture)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	board, err := c.FetchBoard(context.Background(), "0000123456")
	if err != nil {
		t.Fatalf("FetchBoard: %v", err)
	}
	if len(board) != 2 {
		t.Fatalf("board = %d members", len(board))
	}
	if board[0].Name != "JAN KOWALSKI" || board[0].Role != "PREZES ZARZĄDU" {
		t.Errorf("member[0] = %+v", board[0])
	}
}

func TestFetchProfile(t *testing.T) {
	fixture, err := os.ReadFile("testdata/odpis.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/krs/OdpisAktualny/0000123456" {
			http.NotFound(w, r)
			return
		}
		w.Write(fixture)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	p, err := c.FetchProfile(context.Background(), "0000123456")
	if err != nil {
		t.Fatalf("FetchProfile: %v", err)
	}
	if len(p.Board) != 2 {
		t.Fatalf("board = %d members", len(p.Board))
	}
	if p.ShareCapital != "500000,00 PLN" {
		t.Errorf("ShareCapital = %q, want %q", p.ShareCapital, "500000,00 PLN")
	}
}

func TestFetchProfileNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	p, err := c.FetchProfile(context.Background(), "0000000000")
	if err != nil || p != nil {
		t.Errorf("404 should be (nil, nil), got %v, %v", p, err)
	}
}

func TestFetchBoardNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	board, err := c.FetchBoard(context.Background(), "0000000000")
	if err != nil || board != nil {
		t.Errorf("404 should be (nil, nil), got %v, %v", board, err)
	}
}
