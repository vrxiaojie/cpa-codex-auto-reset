package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxResponseBody = 1 << 20

type Client struct {
	baseURL string
	key     string
	http    *http.Client
}

type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("management request failed with status %d", e.StatusCode)
}

func NewClient(baseURL, key string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		key:     strings.TrimSpace(key),
		http:    httpClient,
	}
}

func (c *Client) ResetQuota(ctx context.Context, authIndex string) error {
	if c == nil || c.http == nil || c.baseURL == "" || c.key == "" {
		return errors.New("management client is incomplete")
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return errors.New("auth index is required")
	}
	body, errMarshal := json.Marshal(map[string]string{"auth_index": authIndex})
	if errMarshal != nil {
		return errors.New("encode management request")
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v0/management/reset-quota", bytes.NewReader(body))
	if errRequest != nil {
		return errors.New("create management request")
	}
	request.Header.Set("Authorization", "Bearer "+c.key)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, errDo := c.http.Do(request)
	if errDo != nil {
		return errors.New("management request failed")
	}
	defer response.Body.Close()
	raw, errRead := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if errRead != nil || len(raw) > maxResponseBody {
		return errors.New("invalid management response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPError{StatusCode: response.StatusCode}
	}
	var result struct {
		Status string `json:"status"`
	}
	if errDecode := json.Unmarshal(raw, &result); errDecode != nil || result.Status != "ok" {
		return errors.New("management quota reset was not confirmed")
	}
	return nil
}
