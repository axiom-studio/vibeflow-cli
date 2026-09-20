package vibeflowcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Review credentials belong to the supervisor. These DTOs contain no provider
// secrets and deliberately do not reuse ordinary persona session/cache types.
type reviewRepository struct {
	HeadRepositoryName string `json:"head_repository_name"`
	BaseRepositoryName string `json:"base_repository_name"`
	HeadCloneURL       string `json:"head_clone_url"`
	BaseCloneURL       string `json:"base_clone_url"`
}

type reviewJob struct {
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	ProviderHost     string `json:"provider_host"`
	RepositoryLinkID int64  `json:"repository_link_id"`
	HeadSHA          string `json:"head_sha"`
	BaseSHA          string `json:"base_sha"`
	State            string `json:"state"`
}

type reviewExecution struct {
	Version int       `json:"version"`
	Review  reviewJob `json:"review"`
	Prompt  string    `json:"prompt"`
	Attempt struct {
		ID             string `json:"id"`
		RunnerID       string `json:"runner_id"`
		SessionID      string `json:"session_id"`
		LeaseExpiresAt int64  `json:"lease_expires_at"`
		Round          struct {
			ID         string           `json:"id"`
			JobID      string           `json:"job_id"`
			HeadSHA    string           `json:"head_sha"`
			BaseSHA    string           `json:"base_sha"`
			DeadlineAt int64            `json:"deadline_at"`
			Details    reviewRepository `json:"details"`
		} `json:"round"`
	} `json:"attempt"`
}

type reviewBrief struct {
	RoundID string          `json:"round_id"`
	Digest  string          `json:"digest"`
	Content json.RawMessage `json:"content"`
}

type reviewHTTPError struct{ Status int }

var errReviewConnection = errors.New("review API connection failed")

func (e *reviewHTTPError) Error() string { return fmt.Sprintf("review API returned HTTP %d", e.Status) }

// Unlike the legacy convenience methods, every review call is cancellable,
// bounded, redirect-safe, and never echoes a provider error body or URL token.
func (c *Client) reviewRequest(ctx context.Context, method, path string, body, out any) error {
	base, err := url.Parse(c.baseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || base.Hostname() == "::1"))) {
		return fmt.Errorf("review server must use HTTPS or loopback HTTP without embedded credentials")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+"/rest/v1/vibeflow"+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid review API request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errReviewConnection
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &reviewHTTPError{resp.StatusCode}
	}
	if resp.StatusCode == http.StatusNoContent || out == nil {
		return nil
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return fmt.Errorf("review API response is incomplete or too large")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid review API response")
	}
	return nil
}
