package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type Caller func(method string, payload any) (json.RawMessage, error)

type Client struct {
	call Caller
}

type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

func NewClient(call Caller) *Client {
	return &Client{call: call}
}

func (c *Client) ListAuthFiles() ([]pluginapi.HostAuthFileEntry, error) {
	if c == nil || c.call == nil {
		return nil, errors.New("host callback is unavailable")
	}
	raw, errCall := c.call(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, fmt.Errorf("list host auth files: %w", errCall)
	}
	var response authListResponse
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		return nil, errors.New("decode host auth list response")
	}
	return response.Files, nil
}

func (c *Client) GetAuth(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	if c == nil || c.call == nil {
		return pluginapi.HostAuthGetResponse{}, errors.New("host callback is unavailable")
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return pluginapi.HostAuthGetResponse{}, errors.New("auth index is required")
	}
	raw, errCall := c.call(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("get host auth: %w", errCall)
	}
	var response pluginapi.HostAuthGetResponse
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		return pluginapi.HostAuthGetResponse{}, errors.New("decode host auth response")
	}
	if len(response.JSON) == 0 || !json.Valid(response.JSON) {
		return pluginapi.HostAuthGetResponse{}, errors.New("host auth JSON is invalid")
	}
	return response, nil
}
