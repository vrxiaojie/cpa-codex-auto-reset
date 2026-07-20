package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

var testCredentials = Credentials{AccessToken: "access-secret", AccountID: "account-123"}

func TestUsageRequestContract(t *testing.T) {
	httpClient := testHTTPClient(func(r *http.Request) *http.Response {
		if r.Method != http.MethodGet || r.URL.Path != "/wham/usage" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		assertHeaders(t, r)
		return response(http.StatusOK, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":95,"reset_at":1784563200},"secondary_window":{"used_percent":40,"reset_at":1785168000}},"rate_limit_reset_credits":{"available_count":1}}`)
	})
	client, _ := NewClient("https://example.test", httpClient)
	usage, errUsage := client.Usage(context.Background(), testCredentials)
	if errUsage != nil {
		t.Fatalf("Usage() error = %v", errUsage)
	}
	if usage.Blocked || usage.UsedPercent != 95 || usage.AvailableCount != 1 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestCreditsSelectsEarliestCompleteOpportunity(t *testing.T) {
	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	httpClient := jsonClient(t, http.MethodGet, "/wham/rate-limit-reset-credits", map[string]any{
		"available_count": 2,
		"credits": []map[string]any{
			{"id": "later", "reset_type": "codex_rate_limits", "status": "available", "expires_at": "2026-07-22T00:00:00Z"},
			{"id": "earlier", "reset_type": "codex_rate_limits", "status": "available", "expires_at": "2026-07-21T00:00:00Z"},
		},
	})
	client, _ := NewClient("https://example.test", httpClient)
	credits, errCredits := client.Credits(context.Background(), testCredentials, now)
	if errCredits != nil {
		t.Fatalf("Credits() error = %v", errCredits)
	}
	if len(credits.Available) != 2 || credits.Available[0].ID != "earlier" || credits.Available[0].Ref == "earlier" {
		t.Fatalf("credits = %#v", credits)
	}
}

func TestCreditsFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		body any
	}{
		{name: "incomplete count", body: map[string]any{"available_count": 2, "credits": []map[string]any{{"id": "one", "reset_type": "codex_rate_limits", "status": "available", "expires_at": "2026-07-21T00:00:00Z"}}}},
		{name: "missing expiry", body: map[string]any{"available_count": 1, "credits": []map[string]any{{"id": "one", "reset_type": "codex_rate_limits", "status": "available"}}}},
		{name: "expired", body: map[string]any{"available_count": 1, "credits": []map[string]any{{"id": "one", "reset_type": "codex_rate_limits", "status": "available", "expires_at": "2026-07-19T00:00:00Z"}}}},
		{name: "unknown status", body: map[string]any{"available_count": 0, "credits": []map[string]any{{"id": "one", "reset_type": "codex_rate_limits", "status": "future"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpClient := jsonClient(t, http.MethodGet, "/wham/rate-limit-reset-credits", test.body)
			client, _ := NewClient("https://example.test", httpClient)
			if _, errCredits := client.Credits(context.Background(), testCredentials, now); errCredits == nil {
				t.Fatal("Credits() succeeded")
			}
		})
	}
}

func TestConsumeRequestAndSupportedResults(t *testing.T) {
	httpClient := testHTTPClient(func(r *http.Request) *http.Response {
		if r.Method != http.MethodPost || r.URL.Path != "/wham/rate-limit-reset-credits/consume" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		assertHeaders(t, r)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content type = %q", r.Header.Get("Content-Type"))
		}
		var body map[string]string
		if errDecode := json.NewDecoder(r.Body).Decode(&body); errDecode != nil {
			t.Fatalf("decode body: %v", errDecode)
		}
		if body["redeem_request_id"] != "idem-1" || body["credit_id"] != "credit-1" {
			t.Fatalf("body = %#v", body)
		}
		return response(http.StatusOK, `{"code":"already_redeemed","windows_reset":0}`)
	})
	client, _ := NewClient("https://example.test", httpClient)
	result, errConsume := client.Consume(context.Background(), testCredentials, "credit-1", "idem-1")
	if errConsume != nil {
		t.Fatalf("Consume() error = %v", errConsume)
	}
	if result.Code != ConsumeAlreadyRedeemed {
		t.Fatalf("result = %#v", result)
	}
}

func TestConsumeUnknownResultFailsClosed(t *testing.T) {
	httpClient := jsonClient(t, http.MethodPost, "/wham/rate-limit-reset-credits/consume", map[string]any{"code": "future_result"})
	client, _ := NewClient("https://example.test", httpClient)
	if _, errConsume := client.Consume(context.Background(), testCredentials, "credit-1", "idem-1"); errConsume == nil {
		t.Fatal("Consume() succeeded")
	}
}

func TestConsumeServerErrorIsAmbiguousAndRespectsRetryAfter(t *testing.T) {
	httpClient := testHTTPClient(func(r *http.Request) *http.Response {
		result := response(http.StatusServiceUnavailable, "")
		result.Header.Set("Retry-After", "60")
		return result
	})
	client, _ := NewClient("https://example.test", httpClient)
	_, errConsume := client.Consume(context.Background(), testCredentials, "credit-1", "idem-1")
	var httpErr *HTTPError
	if !errors.As(errConsume, &httpErr) || !httpErr.Ambiguous || httpErr.RetryAfter != time.Minute {
		t.Fatalf("error = %#v", errConsume)
	}
}

func assertHeaders(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Header.Get("Authorization") != "Bearer access-secret" || request.Header.Get("ChatGPT-Account-Id") != "account-123" || request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") != DefaultUserAgent {
		t.Fatalf("headers = %#v", request.Header)
	}
}

func jsonClient(t *testing.T, method, path string, body any) *http.Client {
	t.Helper()
	return testHTTPClient(func(r *http.Request) *http.Response {
		if r.Method != method || r.URL.Path != path {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		assertHeaders(t, r)
		raw, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatalf("marshal response: %v", errMarshal)
		}
		return response(http.StatusOK, string(raw))
	})
}

type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request), nil
}

func testHTTPClient(handler roundTripFunc) *http.Client {
	return &http.Client{Transport: handler}
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
