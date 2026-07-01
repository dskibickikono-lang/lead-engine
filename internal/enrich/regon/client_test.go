package regon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const searchInner = `&lt;root&gt;&lt;dane&gt;&lt;Regon&gt;123456785&lt;/Regon&gt;&lt;Typ&gt;P&lt;/Typ&gt;&lt;Nazwa&gt;STALMET&lt;/Nazwa&gt;&lt;/dane&gt;&lt;/root&gt;`

const reportInner = `&lt;root&gt;&lt;dane&gt;` +
	`&lt;praw_numerTelefonu&gt;221112233&lt;/praw_numerTelefonu&gt;` +
	`&lt;praw_adresEmail&gt;biuro@stalmet.example&lt;/praw_adresEmail&gt;` +
	`&lt;praw_adresStronyinternetowej&gt;stalmet.example&lt;/praw_adresStronyinternetowej&gt;` +
	`&lt;praw_numerWRejestrzeEwidencji&gt;0000123456&lt;/praw_numerWRejestrzeEwidencji&gt;` +
	`&lt;praw_adSiedzMiejscowosc_Nazwa&gt;Warszawa&lt;/praw_adSiedzMiejscowosc_Nazwa&gt;` +
	`&lt;praw_adSiedzUlica_Nazwa&gt;Prosta&lt;/praw_adSiedzUlica_Nazwa&gt;` +
	`&lt;praw_adSiedzNumerNieruchomosci&gt;1&lt;/praw_adSiedzNumerNieruchomosci&gt;` +
	`&lt;praw_adSiedzKodPocztowy&gt;00001&lt;/praw_adSiedzKodPocztowy&gt;` +
	`&lt;praw_liczbaZatrudnionych&gt;85&lt;/praw_liczbaZatrudnionych&gt;` +
	`&lt;praw_podstawowaFormaPrawna_Nazwa&gt;SPÓŁKA Z OGRANICZONĄ ODPOWIEDZIALNOŚCIĄ&lt;/praw_podstawowaFormaPrawna_Nazwa&gt;` +
	`&lt;praw_dataWpisuDoREGON&gt;2009-03-12&lt;/praw_dataWpisuDoREGON&gt;` +
	`&lt;/dane&gt;&lt;/root&gt;`

func soapBody(action, result, inner string) string {
	return fmt.Sprintf(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
<s:Body><%sResponse xmlns="http://CIS/BIR/PUBL/2014/07"><%s>%s</%s></%sResponse></s:Body>
</s:Envelope>`, action, result, inner, result, action)
}

func fakeBIR(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		switch {
		case strings.Contains(s, "Zaloguj"):
			fmt.Fprint(w, soapBody("Zaloguj", "ZalogujResult", "test-sid-123"))
		case strings.Contains(s, "DaneSzukajPodmioty"):
			if r.Header.Get("sid") != "test-sid-123" {
				http.Error(w, "no sid", 403)
				return
			}
			fmt.Fprint(w, soapBody("DaneSzukajPodmioty", "DaneSzukajPodmiotyResult", searchInner))
		case strings.Contains(s, "DanePobierzPelnyRaport"):
			fmt.Fprint(w, soapBody("DanePobierzPelnyRaport", "DanePobierzPelnyRaportResult", reportInner))
		case strings.Contains(s, "Wyloguj"):
			fmt.Fprint(w, soapBody("Wyloguj", "WylogujResult", "true"))
		default:
			http.Error(w, "unknown action", 400)
		}
	}))
}

func TestLookupByNIP(t *testing.T) {
	srv := fakeBIR(t)
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, APIKey: "test-key"}
	rep, err := c.LookupByNIP(context.Background(), "1234567890")
	if err != nil {
		t.Fatalf("LookupByNIP: %v", err)
	}
	if rep.REGON != "123456785" || rep.KRS != "0000123456" {
		t.Errorf("identity: %+v", rep)
	}
	if rep.Phone != "221112233" || rep.Email != "biuro@stalmet.example" || rep.Website != "stalmet.example" {
		t.Errorf("contact: %+v", rep)
	}
	if rep.Address != "Prosta 1, 00-001 Warszawa" {
		t.Errorf("address = %q", rep.Address)
	}
	if rep.Headcount != "85" {
		t.Errorf("headcount = %q, want %q", rep.Headcount, "85")
	}
	if rep.LegalForm != "SPÓŁKA Z OGRANICZONĄ ODPOWIEDZIALNOŚCIĄ" {
		t.Errorf("legal form = %q", rep.LegalForm)
	}
	if rep.RegisteredSince != "2009-03-12" {
		t.Errorf("registered since = %q, want %q", rep.RegisteredSince, "2009-03-12")
	}
}

const errorInner = `&lt;root&gt;&lt;dane&gt;&lt;ErrorCode&gt;4&lt;/ErrorCode&gt;&lt;ErrorMessagePl&gt;Nie znaleziono podmiotu&lt;/ErrorMessagePl&gt;&lt;/dane&gt;&lt;/root&gt;`

func TestLookupByNIPNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		switch {
		case strings.Contains(s, "Zaloguj"):
			fmt.Fprint(w, soapBody("Zaloguj", "ZalogujResult", "test-sid-123"))
		case strings.Contains(s, "DaneSzukajPodmioty"):
			fmt.Fprint(w, soapBody("DaneSzukajPodmioty", "DaneSzukajPodmiotyResult", errorInner))
		case strings.Contains(s, "Wyloguj"):
			fmt.Fprint(w, soapBody("Wyloguj", "WylogujResult", "true"))
		default:
			http.Error(w, "unknown action", 400)
		}
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, APIKey: "k"}
	rep, err := c.LookupByNIP(context.Background(), "9999999999")
	if rep != nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("want (nil, ErrNotFound), got (%+v, %v)", rep, err)
	}
}
