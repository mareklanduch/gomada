package omada

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
	"sync"
	"time"
)

// maxResponseBytes bounds how much of a response is read.
const maxResponseBytes = 16 << 20

// Options configures a Client.
type Options struct {
	// BaseURL is the controller address, e.g. "https://192.168.0.2:8043".
	BaseURL string
	// ControllerID is the controller's omadacId.
	ControllerID string
	// ClientID identifies the Open API application.
	ClientID string
	// Secret returns the client secret. It is called only when the client
	// has to log in, so a valid cached token never needs the secret.
	Secret func(context.Context) (string, error)
	// HTTPClient makes the requests. Nil means NewHTTPClient with defaults.
	HTTPClient *http.Client
	// Cache optionally keeps the access token between runs.
	Cache TokenCache
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Client talks to one controller. It is safe for concurrent use.
type Client struct {
	baseURL      string
	controllerID string
	clientID     string
	secret       func(context.Context) (string, error)
	http         *http.Client
	cache        TokenCache
	now          func() time.Time

	mu       sync.Mutex
	token    Token
	rejected string // last access token the controller refused
}

// New returns a client for the controller described by opts.
func New(opts Options) *Client {
	c := &Client{
		baseURL:      strings.TrimRight(opts.BaseURL, "/"),
		controllerID: opts.ControllerID,
		clientID:     opts.ClientID,
		secret:       opts.Secret,
		http:         opts.HTTPClient,
		cache:        opts.Cache,
		now:          opts.Now,
	}
	if c.http == nil {
		c.http = NewHTTPClient(TLSOptions{}, 0)
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

// Info describes a controller. It is available without logging in.
type Info struct {
	ControllerID string `json:"omadacId"`
	Version      string `json:"controllerVer"`
	APIVersion   string `json:"apiVer"`
}

// FetchInfo asks the controller at baseURL for its ID and version.
func FetchInfo(ctx context.Context, hc *http.Client, baseURL string) (Info, error) {
	var info Info
	u := strings.TrimRight(baseURL, "/") + "/api/info"
	if err := do(ctx, hc, http.MethodGet, u, "", nil, &info); err != nil {
		return Info{}, err
	}
	if info.ControllerID == "" {
		return Info{}, errors.New("the address answered, but not like an Omada controller (no controller ID)")
	}
	return info, nil
}

// Login makes sure the client holds a usable access token, logging in if
// necessary. Calling it is optional; every request does the same.
func (c *Client) Login(ctx context.Context) error {
	_, err := c.accessToken(ctx)
	return err
}

// accessToken returns a token that is valid for at least a minute. It uses,
// in order: the token in memory, the cached token, a fresh login.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.token.Valid(now) {
		return c.token.AccessToken, nil
	}
	if c.cache != nil {
		if t, ok := c.cache.Load(); ok && t.Valid(now) && t.AccessToken != c.rejected {
			c.token = t
			return t.AccessToken, nil
		}
	}

	t, err := c.login(ctx)
	if err != nil {
		return "", err
	}
	c.token = t
	if c.cache != nil {
		c.cache.Save(t)
	}
	return t.AccessToken, nil
}

func (c *Client) login(ctx context.Context) (Token, error) {
	if c.secret == nil {
		return Token{}, fmt.Errorf("%w: no client secret available", ErrAuth)
	}
	secret, err := c.secret(ctx)
	if err != nil {
		return Token{}, err
	}
	var res struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   Num    `json:"expiresIn"`
	}
	body := map[string]string{
		"omadacId":      c.controllerID,
		"client_id":     c.clientID,
		"client_secret": secret,
	}
	u := c.baseURL + "/openapi/authorize/token?grant_type=client_credentials"
	if err := do(ctx, c.http, http.MethodPost, u, "", body, &res); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if apiErr.HTTPStatus == http.StatusNotFound {
				return Token{}, fmt.Errorf("the controller has no Open API at this address (HTTP 404); check the address and that this controller version offers Settings > Platform Integration > Open API: %w", err)
			}
			return Token{}, fmt.Errorf("%w: %w", ErrAuth, err)
		}
		return Token{}, err
	}
	if res.AccessToken == "" {
		return Token{}, fmt.Errorf("%w: the controller returned no access token", ErrAuth)
	}
	lifetime := time.Duration(float64(res.ExpiresIn) * float64(time.Second))
	if lifetime <= 0 {
		lifetime = fallbackTokenLifetime
	}
	return Token{AccessToken: res.AccessToken, ExpiresAt: c.now().Add(lifetime)}, nil
}

// reject forgets a token the controller refused, so the next request does
// not reuse it from memory or from the cache.
func (c *Client) reject(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rejected = token
	if c.token.AccessToken == token {
		c.token = Token{}
	}
	if c.cache != nil {
		if t, ok := c.cache.Load(); ok && t.AccessToken == token {
			c.cache.Clear()
		}
	}
}

// call makes an authenticated request below /openapi/v1/{controllerID}. When
// the controller rejects the token it logs in again and repeats the request
// once.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	u := c.baseURL + "/openapi/v1/" + url.PathEscape(c.controllerID) + path
	for attempt := 0; ; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		err = do(ctx, c.http, method, u, token, body, out)
		if attempt == 0 && tokenRejected(err) {
			c.reject(token)
			continue
		}
		return err
	}
}

// maxPages bounds list requests against a controller that never stops paging.
const maxPages = 1000

// list fetches every page of a paged endpoint.
func list[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	all := []T{}
	for page := 1; page <= maxPages; page++ {
		var res struct {
			TotalRows int `json:"totalRows"`
			Data      []T `json:"data"`
		}
		p := fmt.Sprintf("%s?page=%d&pageSize=100", path, page)
		if err := c.call(ctx, http.MethodGet, p, nil, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Data...)
		if len(res.Data) == 0 || len(all) >= res.TotalRows {
			break
		}
	}
	return all, nil
}

// envelope is the wrapper around every controller response.
type envelope struct {
	ErrorCode int             `json:"errorCode"`
	Msg       string          `json:"msg"`
	Result    json.RawMessage `json:"result"`
}

// do makes one request and decodes the result of the response into out.
func do(ctx context.Context, hc *http.Client, method, u, token string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "AccessToken="+token)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return classify(ctx, err)
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do about a failed close
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return classify(ctx, err)
	}

	var env envelope
	if jsonErr := json.Unmarshal(raw, &env); jsonErr != nil {
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return &APIError{Message: http.StatusText(resp.StatusCode), HTTPStatus: resp.StatusCode}
		}
		return fmt.Errorf("the controller sent a response that is not JSON: %w", jsonErr)
	}
	if env.ErrorCode != 0 {
		return &APIError{Code: env.ErrorCode, Message: env.Msg, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := env.Msg
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return &APIError{Message: msg, HTTPStatus: resp.StatusCode}
	}
	if out == nil || len(env.Result) == 0 || string(env.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
