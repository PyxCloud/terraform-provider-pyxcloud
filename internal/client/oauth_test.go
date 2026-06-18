package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestClientCredentialsTokenFetchAndCache(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "client_credentials" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("client_id") != "tf-prov" || r.Form.Get("client_secret") != "sek" {
			t.Errorf("client creds not sent: %v", r.Form)
		}
		_, _ = w.Write([]byte(`{"access_token":"AT-1","expires_in":3600}`))
	}))
	defer srv.Close()

	src := newClientCredentialsSource(srv.URL, "tf-prov", "sek")
	tok, err := src.token(context.Background())
	if err != nil || tok != "AT-1" {
		t.Fatalf("token = %q err=%v", tok, err)
	}
	// Second call within TTL must be served from cache (no extra HTTP call).
	if _, err := src.token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected 1 token fetch (cached), got %d", got)
	}
}

func TestClientCredentialsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	defer srv.Close()
	_, err := newClientCredentialsSource(srv.URL, "x", "y").token(context.Background())
	if err == nil {
		t.Fatal("expected error on 401 client_credentials")
	}
}

func TestHTTPClientUsesMachineAuth(t *testing.T) {
	var tokenHits, apiHits int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenHits, 1)
		_, _ = w.Write([]byte(`{"access_token":"machine-AT","expires_in":3600}`))
	}))
	defer tokenSrv.Close()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&apiHits, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer machine-AT" {
			t.Errorf("api auth header = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"t1","name":"n"}`))
	}))
	defer apiSrv.Close()

	c := NewHTTP(Config{Endpoint: apiSrv.URL, ClientID: "id", ClientSecret: "sec", TokenURL: tokenSrv.URL})
	if _, err := c.CreateTopology(context.Background(), sampleTopology()); err != nil {
		t.Fatalf("CreateTopology: %v", err)
	}
	if atomic.LoadInt32(&tokenHits) != 1 || atomic.LoadInt32(&apiHits) != 1 {
		t.Errorf("tokenHits=%d apiHits=%d", tokenHits, apiHits)
	}
}
