package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL   = "https://chatgpt.com/backend-api"
	DefaultUserAgent = "codex-cli"
	maxResponseBody  = 1 << 20
)

type Client struct {
	baseURL   string
	http      *http.Client
	userAgent string
}

type Credentials struct {
	AccessToken string
	AccountID   string
}

type Usage struct {
	Allowed        bool
	Blocked        bool
	UsedPercent    float64
	Primary        *Window
	Secondary      *Window
	AvailableCount int
}

type Window struct {
	UsedPercent float64
	ResetAt     time.Time
}

type Credit struct {
	ID        string
	Ref       string
	ExpiresAt time.Time
}

type CreditList struct {
	AvailableCount int
	Available      []Credit
}

type ConsumeCode string

const (
	ConsumeReset           ConsumeCode = "reset"
	ConsumeAlreadyRedeemed ConsumeCode = "already_redeemed"
	ConsumeNothingToReset  ConsumeCode = "nothing_to_reset"
	ConsumeNoCredit        ConsumeCode = "no_credit"
)

type ConsumeResult struct {
	Code         ConsumeCode
	WindowsReset int
}

type HTTPError struct {
	StatusCode int
	RetryAfter time.Duration
	Ambiguous  bool
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("codex request failed with status %d", e.StatusCode)
}

func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, errParse := url.Parse(baseURL)
	if errParse != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("invalid Codex base URL")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: baseURL, http: httpClient, userAgent: DefaultUserAgent}, nil
}

func (c *Client) Usage(ctx context.Context, credentials Credentials) (Usage, error) {
	raw, _, errDo := c.do(ctx, http.MethodGet, "/wham/usage", credentials, nil, false)
	if errDo != nil {
		return Usage{}, errDo
	}
	var response struct {
		RateLimit *struct {
			Allowed         *bool      `json:"allowed"`
			LimitReached    *bool      `json:"limit_reached"`
			PrimaryWindow   *rawWindow `json:"primary_window"`
			SecondaryWindow *rawWindow `json:"secondary_window"`
		} `json:"rate_limit"`
		RateLimitReachedType json.RawMessage `json:"rate_limit_reached_type"`
		ResetCredits         *struct {
			AvailableCount *int `json:"available_count"`
		} `json:"rate_limit_reset_credits"`
	}
	if errDecode := decodeJSON(raw, &response); errDecode != nil {
		return Usage{}, errors.New("invalid Codex usage response")
	}
	if response.RateLimit == nil || response.RateLimit.Allowed == nil || response.RateLimit.LimitReached == nil {
		return Usage{}, errors.New("incomplete Codex usage response")
	}
	primary, errPrimary := parseWindow(response.RateLimit.PrimaryWindow)
	if errPrimary != nil {
		return Usage{}, errPrimary
	}
	secondary, errSecondary := parseWindow(response.RateLimit.SecondaryWindow)
	if errSecondary != nil {
		return Usage{}, errSecondary
	}
	if primary == nil && secondary == nil {
		return Usage{}, errors.New("Codex usage response has no rate-limit windows")
	}
	used := 0.0
	for _, window := range []*Window{primary, secondary} {
		if window != nil && window.UsedPercent > used {
			used = window.UsedPercent
		}
	}
	blocked := *response.RateLimit.LimitReached || !*response.RateLimit.Allowed || hasReachedType(response.RateLimitReachedType)
	availableCount := 0
	if response.ResetCredits != nil {
		if response.ResetCredits.AvailableCount == nil || *response.ResetCredits.AvailableCount < 0 {
			return Usage{}, errors.New("invalid reset credit summary")
		}
		availableCount = *response.ResetCredits.AvailableCount
	}
	return Usage{
		Allowed:        *response.RateLimit.Allowed,
		Blocked:        blocked,
		UsedPercent:    used,
		Primary:        primary,
		Secondary:      secondary,
		AvailableCount: availableCount,
	}, nil
}

func (c *Client) Credits(ctx context.Context, credentials Credentials, now time.Time) (CreditList, error) {
	raw, _, errDo := c.do(ctx, http.MethodGet, "/wham/rate-limit-reset-credits", credentials, nil, false)
	if errDo != nil {
		return CreditList{}, errDo
	}
	var response struct {
		Credits []struct {
			ID        string  `json:"id"`
			ResetType string  `json:"reset_type"`
			Status    string  `json:"status"`
			ExpiresAt *string `json:"expires_at"`
		} `json:"credits"`
		AvailableCount *int `json:"available_count"`
	}
	if errDecode := decodeJSON(raw, &response); errDecode != nil || response.AvailableCount == nil || *response.AvailableCount < 0 {
		return CreditList{}, errors.New("invalid reset credit response")
	}
	availableDetails := 0
	available := make([]Credit, 0)
	seen := make(map[string]struct{})
	for _, item := range response.Credits {
		status := strings.TrimSpace(item.Status)
		switch status {
		case "available", "redeeming", "redeemed":
		default:
			return CreditList{}, errors.New("unknown reset credit status")
		}
		if status != "available" {
			continue
		}
		availableDetails++
		id := strings.TrimSpace(item.ID)
		if id == "" || item.ExpiresAt == nil || strings.TrimSpace(*item.ExpiresAt) == "" {
			return CreditList{}, errors.New("available reset credit is incomplete")
		}
		if _, exists := seen[id]; exists {
			return CreditList{}, errors.New("duplicate reset credit ID")
		}
		seen[id] = struct{}{}
		expiresAt, errParse := time.Parse(time.RFC3339, strings.TrimSpace(*item.ExpiresAt))
		if errParse != nil {
			return CreditList{}, errors.New("invalid reset credit expiration")
		}
		if !expiresAt.After(now) {
			return CreditList{}, errors.New("reset credit is expired")
		}
		if strings.TrimSpace(item.ResetType) == "codex_rate_limits" {
			available = append(available, Credit{ID: id, Ref: hashRef("credit", id), ExpiresAt: expiresAt.UTC()})
		}
	}
	if *response.AvailableCount != availableDetails {
		return CreditList{}, errors.New("reset credit list is incomplete")
	}
	sort.Slice(available, func(i, j int) bool {
		if available[i].ExpiresAt.Equal(available[j].ExpiresAt) {
			return available[i].ID < available[j].ID
		}
		return available[i].ExpiresAt.Before(available[j].ExpiresAt)
	})
	return CreditList{AvailableCount: *response.AvailableCount, Available: available}, nil
}

func (c *Client) Consume(ctx context.Context, credentials Credentials, creditID, idempotencyKey string) (ConsumeResult, error) {
	creditID = strings.TrimSpace(creditID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if creditID == "" || idempotencyKey == "" {
		return ConsumeResult{}, errors.New("credit ID and idempotency key are required")
	}
	body, errMarshal := json.Marshal(map[string]string{
		"redeem_request_id": idempotencyKey,
		"credit_id":         creditID,
	})
	if errMarshal != nil {
		return ConsumeResult{}, errors.New("encode reset request")
	}
	raw, _, errDo := c.do(ctx, http.MethodPost, "/wham/rate-limit-reset-credits/consume", credentials, body, true)
	if errDo != nil {
		return ConsumeResult{}, errDo
	}
	var response struct {
		Code         ConsumeCode `json:"code"`
		WindowsReset int         `json:"windows_reset"`
	}
	if errDecode := decodeJSON(raw, &response); errDecode != nil {
		return ConsumeResult{}, errors.New("invalid reset consume response")
	}
	switch response.Code {
	case ConsumeReset, ConsumeAlreadyRedeemed, ConsumeNothingToReset, ConsumeNoCredit:
	default:
		return ConsumeResult{}, errors.New("unknown reset consume result")
	}
	return ConsumeResult{Code: response.Code, WindowsReset: response.WindowsReset}, nil
}

func (c *Client) do(ctx context.Context, method, path string, credentials Credentials, body []byte, write bool) ([]byte, http.Header, error) {
	if c == nil || c.http == nil {
		return nil, nil, errors.New("Codex client is unavailable")
	}
	credentials.AccessToken = strings.TrimSpace(credentials.AccessToken)
	credentials.AccountID = strings.TrimSpace(credentials.AccountID)
	if credentials.AccessToken == "" || credentials.AccountID == "" {
		return nil, nil, errors.New("Codex credentials are incomplete")
	}
	request, errRequest := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if errRequest != nil {
		return nil, nil, errors.New("create Codex request")
	}
	request.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	request.Header.Set("ChatGPT-Account-Id", credentials.AccountID)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, errDo := c.http.Do(request)
	if errDo != nil {
		if write {
			return nil, nil, &HTTPError{Ambiguous: true}
		}
		return nil, nil, errors.New("Codex request failed")
	}
	defer response.Body.Close()
	raw, errRead := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if errRead != nil {
		return nil, nil, errors.New("read Codex response")
	}
	if len(raw) > maxResponseBody {
		return nil, nil, errors.New("Codex response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.Header.Clone(), &HTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
			Ambiguous:  write && response.StatusCode >= 500,
		}
	}
	return raw, response.Header.Clone(), nil
}

type rawWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	ResetAt     *int64   `json:"reset_at"`
}

func parseWindow(raw *rawWindow) (*Window, error) {
	if raw == nil {
		return nil, nil
	}
	if raw.UsedPercent == nil || raw.ResetAt == nil || *raw.UsedPercent < 0 || *raw.UsedPercent > 100 || *raw.ResetAt <= 0 {
		return nil, errors.New("invalid Codex rate-limit window")
	}
	return &Window{UsedPercent: *raw.UsedPercent, ResetAt: time.Unix(*raw.ResetAt, 0).UTC()}, nil
}

func decodeJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if errDecode := decoder.Decode(target); errDecode != nil {
		return errDecode
	}
	var extra any
	if errExtra := decoder.Decode(&extra); !errors.Is(errExtra, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func hasReachedType(raw json.RawMessage) bool {
	text := strings.TrimSpace(string(raw))
	return text != "" && text != "null" && text != "{}"
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, errParse := strconv.Atoi(value); errParse == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if timestamp, errParse := http.ParseTime(value); errParse == nil && timestamp.After(now) {
		return timestamp.Sub(now)
	}
	return 0
}
