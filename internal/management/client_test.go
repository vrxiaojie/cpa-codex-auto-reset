package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestResetQuotaContract(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/v0/management/reset-quota" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer management-secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var body map[string]string
		if errDecode := json.NewDecoder(request.Body).Decode(&body); errDecode != nil {
			t.Fatalf("decode body: %v", errDecode)
		}
		if body["auth_index"] != "auth-index-1" {
			t.Fatalf("body = %#v", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`))}, nil
	})}
	client := NewClient("http://127.0.0.1:8317", "management-secret", httpClient)
	if errReset := client.ResetQuota(context.Background(), "auth-index-1"); errReset != nil {
		t.Fatalf("ResetQuota() error = %v", errReset)
	}
}
