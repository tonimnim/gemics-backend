package mpesa

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Provider interface {
	Initiate(context.Context, InitiateRequest) (InitiateResponse, error)
	Query(context.Context, string) (QueryResponse, error)
}

type Config struct {
	Environment, ConsumerKey, ConsumerSecret, ShortCode, Passkey string
	CallbackURL, TransactionType                                 string
	BaseURL                                                      string
	Timeout                                                      time.Duration
}

type InitiateRequest struct {
	PhoneNumber      string
	AmountKES        int64
	AccountReference string
	Description      string
}

type InitiateResponse struct {
	MerchantRequestID   string       `json:"MerchantRequestID"`
	CheckoutRequestID   string       `json:"CheckoutRequestID"`
	ResponseCode        darajaString `json:"ResponseCode"`
	ResponseDescription string       `json:"ResponseDescription"`
	CustomerMessage     string       `json:"CustomerMessage"`
}

type QueryResponse struct {
	MerchantRequestID   string       `json:"MerchantRequestID"`
	CheckoutRequestID   string       `json:"CheckoutRequestID"`
	ResponseCode        darajaString `json:"ResponseCode"`
	ResponseDescription string       `json:"ResponseDescription"`
	ResultCode          darajaString `json:"ResultCode"`
	ResultDesc          string       `json:"ResultDesc"`
}

// nairobi is East Africa Time. Daraja checks the STK password against a
// timestamp in Kenyan local time; Kenya has no daylight saving, so a fixed
// offset is exact and needs no tzdata in the container.
var nairobi = time.FixedZone("EAT", 3*60*60)

// darajaTimestamp is the YYYYMMDDHHmmss timestamp Daraja expects.
func (c *Client) darajaTimestamp() string {
	return c.now().In(nairobi).Format("20060102150405")
}

type darajaString string

func (value *darajaString) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		*value = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		*value = darajaString(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return fmt.Errorf("decode Daraja value: %w", err)
	}
	*value = darajaString(number.String())
	return nil
}

type Client struct {
	config         Config
	http           *http.Client
	now            func() time.Time
	mu             sync.Mutex
	token          string
	tokenExpiresAt time.Time
}

func New(config Config) (*Client, error) {
	if config.Environment != "sandbox" && config.Environment != "production" {
		return nil, fmt.Errorf("unsupported M-Pesa environment %q", config.Environment)
	}
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Second
	}
	if config.ConsumerKey == "" || config.ConsumerSecret == "" || config.ShortCode == "" || config.Passkey == "" || config.CallbackURL == "" || config.TransactionType == "" {
		return nil, errors.New("incomplete M-Pesa client configuration")
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		MaxConnsPerHost:       50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &Client{config: config, http: &http.Client{Transport: transport, Timeout: config.Timeout}, now: time.Now}, nil
}

func (c *Client) Initiate(ctx context.Context, request InitiateRequest) (InitiateResponse, error) {
	if request.AmountKES < 1 || len(request.AccountReference) < 1 || len(request.AccountReference) > 12 || len(request.Description) < 1 || len(request.Description) > 13 {
		return InitiateResponse{}, errors.New("invalid M-Pesa STK request amount or reference fields")
	}
	timestamp := c.darajaTimestamp()
	password := base64.StdEncoding.EncodeToString([]byte(c.config.ShortCode + c.config.Passkey + timestamp))
	payload := map[string]any{
		"BusinessShortCode": c.config.ShortCode,
		"Password":          password,
		"Timestamp":         timestamp,
		"TransactionType":   c.config.TransactionType,
		"Amount":            request.AmountKES,
		"PartyA":            request.PhoneNumber,
		"PartyB":            c.config.ShortCode,
		"PhoneNumber":       request.PhoneNumber,
		"CallBackURL":       c.config.CallbackURL,
		"AccountReference":  request.AccountReference,
		"TransactionDesc":   request.Description,
	}
	var response InitiateResponse
	if err := c.post(ctx, "/mpesa/stkpush/v1/processrequest", payload, &response); err != nil {
		return InitiateResponse{}, err
	}
	if response.ResponseCode != "0" || response.CheckoutRequestID == "" {
		return InitiateResponse{}, fmt.Errorf("M-Pesa rejected STK request: %s", response.ResponseDescription)
	}
	return response, nil
}

func (c *Client) Query(ctx context.Context, checkoutRequestID string) (QueryResponse, error) {
	if strings.TrimSpace(checkoutRequestID) == "" {
		return QueryResponse{}, errors.New("M-Pesa checkout request ID is required")
	}
	timestamp := c.darajaTimestamp()
	password := base64.StdEncoding.EncodeToString([]byte(c.config.ShortCode + c.config.Passkey + timestamp))
	payload := map[string]any{"BusinessShortCode": c.config.ShortCode, "Password": password, "Timestamp": timestamp, "CheckoutRequestID": checkoutRequestID}
	var response QueryResponse
	if err := c.post(ctx, "/mpesa/stkpushquery/v1/query", payload, &response); err != nil {
		return QueryResponse{}, err
	}
	return response, nil
}

func (c *Client) post(ctx context.Context, path string, payload, destination any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, destination)
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.tokenExpiresAt.Sub(c.now()) > time.Minute {
		return c.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+"/oauth/v1/generate?grant_type=client_credentials", nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.config.ConsumerKey, c.config.ConsumerSecret)
	var response struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := c.do(req, &response); err != nil {
		return "", err
	}
	if response.AccessToken == "" {
		return "", errors.New("M-Pesa OAuth response omitted access token")
	}
	expires := 3599
	raw := strings.Trim(string(response.ExpiresIn), `"`)
	if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
		expires = parsed
	}
	c.token = response.AccessToken
	c.tokenExpiresAt = c.now().Add(time.Duration(expires) * time.Second)
	return c.token, nil
}

func (c *Client) do(req *http.Request, destination any) error {
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		var apiError struct {
			ErrorCode    string `json:"errorCode"`
			ErrorMessage string `json:"errorMessage"`
		}
		_ = json.Unmarshal(body, &apiError)
		if apiError.ErrorMessage != "" {
			return fmt.Errorf("M-Pesa API %s: %s", apiError.ErrorCode, apiError.ErrorMessage)
		}
		return fmt.Errorf("M-Pesa API returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(destination)
}

func (c *Client) baseURL() string {
	if c.config.BaseURL != "" {
		return strings.TrimRight(c.config.BaseURL, "/")
	}
	if c.config.Environment == "production" {
		return "https://api.safaricom.co.ke"
	}
	return "https://sandbox.safaricom.co.ke"
}
