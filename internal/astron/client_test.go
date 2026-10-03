package astron

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testSession() *Session {
	return &Session{AccountID: "acc-1", UID: "1", Token: "tok", SSOSessionID: "sso"}
}

func TestInitTenantApp(t *testing.T) {
	var (
		gotPath, gotMethod, gotCookie, gotClientType, gotVersion string
		gotBodyLen                                               int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotCookie = r.Header.Get("Cookie")
		gotClientType = r.Header.Get("clientType")
		gotVersion = r.Header.Get("studioVersion")
		body, _ := io.ReadAll(r.Body)
		gotBodyLen = len(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"flag":true,"code":0,"desc":"成功","data":{"banned":false}}`)
	}))
	defer srv.Close()

	c := NewClient("https://upstream.invalid/v1", "https://models.invalid", srv.URL, "3.4.4")
	got, err := c.InitTenantApp(context.Background(), testSession())
	if err != nil {
		t.Fatalf("InitTenantApp returned error: %v", err)
	}
	if got == nil || got.Banned {
		t.Fatalf("InitTenantApp = %+v, want Banned=false", got)
	}
	if gotPath != "/tenant-app/v2/init-app" {
		t.Fatalf("path = %q, want /tenant-app/v2/init-app", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotBodyLen != 0 {
		t.Fatalf("body length = %d, want 0", gotBodyLen)
	}
	if gotCookie == "" {
		t.Fatal("session cookie was not sent")
	}
	if gotClientType != ClientType() {
		t.Fatalf("clientType = %q, want %q", gotClientType, ClientType())
	}
	if gotVersion != "3.4.4" {
		t.Fatalf("studioVersion = %q, want 3.4.4", gotVersion)
	}
}

func TestInitTenantAppBanned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"flag":true,"code":0,"data":{"banned":true}}`)
	}))
	defer srv.Close()

	c := NewClient("", "", srv.URL, "3.4.4")
	got, err := c.InitTenantApp(context.Background(), testSession())
	if err != nil {
		t.Fatalf("InitTenantApp returned error: %v", err)
	}
	if !got.Banned {
		t.Fatal("Banned = false, want true")
	}
}

func TestInitTenantAppBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"flag":false,"code":1001,"desc":"boom"}`)
	}))
	defer srv.Close()

	c := NewClient("", "", srv.URL, "3.4.4")
	if _, err := c.InitTenantApp(context.Background(), testSession()); err == nil {
		t.Fatal("expected an error for flag=false")
	} else {
		var envErr *EnvelopeError
		if !errors.As(err, &envErr) || envErr.Desc != "boom" {
			t.Fatalf("error = %v, want EnvelopeError{Desc: boom}", err)
		}
	}
}

func TestInitTenantAppSessionExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"flag":false,"code":11001001,"desc":"session expired"}`)
	}))
	defer srv.Close()

	c := NewClient("", "", srv.URL, "3.4.4")
	if _, err := c.InitTenantApp(context.Background(), testSession()); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("error = %v, want ErrSessionExpired", err)
	}
}
