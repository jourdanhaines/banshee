package steam

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStoreSearchRequestShape(t *testing.T) {
	var gotPath, gotTerm, gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTerm = r.URL.Query().Get("term")
		gotLang = r.URL.Query().Get("l")
		io.WriteString(w, `{"total":0,"items":[]}`)
	}))
	defer srv.Close()
	p := New(substringScorer, WithSteamRoot(t.TempDir()),
		WithStoreBaseURL(srv.URL), WithStoreClient(srv.Client()))

	if _, err := p.storeSearch(context.Background(), "hollow knight & co"); err != nil {
		t.Fatalf("storeSearch: %v", err)
	}
	if gotPath != "/api/storesearch/" {
		t.Errorf("path = %q", gotPath)
	}
	if gotTerm != "hollow knight & co" {
		t.Errorf("term = %q", gotTerm)
	}
	if gotLang != "english" {
		t.Errorf("l = %q", gotLang)
	}
}

func TestStoreSearchFiltersAndErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    int
		wantErr bool
	}{
		{
			name:   "app-typed items only",
			status: 200,
			body: `{"total":3,"items":[
				{"type":"app","name":"A","id":1},
				{"type":"music","name":"B","id":2},
				{"type":"app","name":"","id":3},
				{"type":"app","name":"D","id":0},
				{"type":"app","name":"E","id":5,"unknown_field":true}
			]}`,
			want: 2,
		},
		{name: "server error", status: 500, body: `boom`, wantErr: true},
		{name: "malformed body", status: 200, body: `{"items":`, wantErr: true},
		{name: "empty result", status: 200, body: `{"total":0,"items":[]}`, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			p := New(substringScorer, WithSteamRoot(t.TempDir()),
				WithStoreBaseURL(srv.URL), WithStoreClient(srv.Client()))
			items, err := p.storeSearch(context.Background(), "x")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("storeSearch = %v, want error", items)
				}
				return
			}
			if err != nil {
				t.Fatalf("storeSearch: %v", err)
			}
			if len(items) != tc.want {
				t.Fatalf("items = %v, want %d", items, tc.want)
			}
		})
	}
}

func TestStoreRowsCappedByMaxResults(t *testing.T) {
	srv := jsonStore(t, `{"total":3,"items":[
		{"type":"app","name":"A","id":1},
		{"type":"app","name":"B","id":2},
		{"type":"app","name":"C","id":3}
	]}`)
	p := newTestProvider(t, nil, WithMaxResults(2),
		WithStoreBaseURL(srv.URL), WithStoreClient(srv.Client()))
	got, err := p.Query(context.Background(), "steam anything")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// 2 store rows + the search row.
	if len(got) != 3 {
		t.Fatalf("results = %v, want 2 store rows + search", titles(got))
	}
	if got[0].Score != StoreScore || got[1].Score != StoreScore-1 {
		t.Errorf("store scores = %d, %d", got[0].Score, got[1].Score)
	}
}

func TestFormatPrice(t *testing.T) {
	cases := []struct {
		name string
		in   *storePrice
		want string
	}{
		{"nil is free", nil, "Free"},
		{"zero is free", &storePrice{Currency: "USD", Final: 0}, "Free"},
		{"usd", &storePrice{Currency: "USD", Final: 1499}, "$14.99"},
		{"usd under a dollar", &storePrice{Currency: "USD", Final: 99}, "$0.99"},
		{"other currency", &storePrice{Currency: "EUR", Final: 1950}, "19.50 EUR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatPrice(tc.in); got != tc.want {
				t.Errorf("formatPrice = %q, want %q", got, tc.want)
			}
		})
	}
}
