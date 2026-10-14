package telephony

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIBase is Twilio's REST API.
const DefaultAPIBase = "https://api.twilio.com"

// Client is a small client of Twilio's 2010-04-01 REST API: what the
// setup and the calls need. Goroutine-safe.
type Client struct {
	AccountSID string
	AuthToken  string
	// Base is the API's origin; "" = DefaultAPIBase (tests point it at a
	// fake).
	Base string
	// HTTP is the client (the app's, with its proxy); nil = a default
	// one with a 20 s timeout.
	HTTP *http.Client
}

var sidRE = regexp.MustCompile(`^AC[0-9a-fA-F]{32}$`)

// ValidAccountSID reports whether s looks like an Account SID: "AC" and
// 32 hexadecimal digits.
func ValidAccountSID(s string) bool { return sidRE.MatchString(s) }

// APIError is an error answer of Twilio's API.
type APIError struct {
	Status  int    // HTTP status
	Code    int    `json:"code"` // Twilio's error code (21211: invalid To…)
	Message string `json:"message"`
	MoreURL string `json:"more_info"`
}

func (e *APIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("Twilio error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("Twilio answered %d: %s", e.Status, e.Message)
}

// ErrUnauthorized: the Account SID or auth token is wrong.
var ErrUnauthorized = errors.New("twilio: the Account SID or auth token is not accepted")

// Unauthorized reports whether err means the credentials were refused.
func Unauthorized(err error) bool {
	var ae *APIError
	return errors.Is(err, ErrUnauthorized) || errors.As(err, &ae) && ae.Status == http.StatusUnauthorized
}

func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return DefaultAPIBase
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// do sends a request to path (under /2010-04-01/Accounts/<sid>) and
// decodes the JSON answer into out.
func (c *Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	if c.AccountSID == "" || c.AuthToken == "" {
		return ErrUnauthorized
	}
	u := c.base() + "/2010-04-01/Accounts/" + url.PathEscape(c.AccountSID) + path
	var body io.Reader
	if form != nil && method != http.MethodGet {
		body = strings.NewReader(form.Encode())
	} else if form != nil {
		u += "?" + form.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.AccountSID, c.AuthToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		ae := &APIError{Status: resp.StatusCode}
		if json.Unmarshal(data, ae) != nil || ae.Message == "" {
			ae.Message = strings.TrimSpace(string(data))
			if len(ae.Message) > 200 {
				ae.Message = ae.Message[:200]
			}
			if ae.Message == "" {
				ae.Message = http.StatusText(resp.StatusCode)
			}
		}
		ae.Status = resp.StatusCode
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w: %w", ErrUnauthorized, ae)
		}
		return ae
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Account is the account the credentials open.
type Account struct {
	SID    string `json:"sid"`
	Name   string `json:"friendly_name"`
	Status string `json:"status"` // "active", "suspended", "closed"
	Type   string `json:"type"`   // "Trial" or "Full"
}

// Account checks the credentials by reading the account.
func (c *Client) Account(ctx context.Context) (Account, error) {
	var a Account
	err := c.do(ctx, http.MethodGet, ".json", nil, &a)
	return a, err
}

// Number is one of the account's phone numbers.
type Number struct {
	SID          string `json:"sid"`
	Number       string `json:"phone_number"` // E.164
	Name         string `json:"friendly_name"`
	VoiceURL     string `json:"voice_url"`
	VoiceMethod  string `json:"voice_method"`
	Capabilities struct {
		Voice bool `json:"voice"`
		SMS   bool `json:"sms"`
	} `json:"capabilities"`
}

// Numbers lists the account's phone numbers (up to 200).
func (c *Client) Numbers(ctx context.Context) ([]Number, error) {
	var page struct {
		Numbers []Number `json:"incoming_phone_numbers"`
	}
	err := c.do(ctx, http.MethodGet, "/IncomingPhoneNumbers.json", url.Values{"PageSize": {"200"}}, &page)
	return page.Numbers, err
}

// SetVoiceWebhook points a number's incoming calls at voiceURL (POST)
// and its call status events at statusURL ("" = leave it).
func (c *Client) SetVoiceWebhook(ctx context.Context, numberSID, voiceURL, statusURL string) (Number, error) {
	form := url.Values{"VoiceUrl": {voiceURL}, "VoiceMethod": {"POST"}}
	if statusURL != "" {
		form.Set("StatusCallback", statusURL)
		form.Set("StatusCallbackMethod", "POST")
	}
	var n Number
	err := c.do(ctx, http.MethodPost, "/IncomingPhoneNumbers/"+url.PathEscape(numberSID)+".json", form, &n)
	return n, err
}

// CallRequest places a call.
type CallRequest struct {
	To, From string
	// URL is the voice webhook Twilio fetches when the call is answered
	// (POST); StatusURL gets the call's progress (initiated, ringing,
	// answered, completed).
	URL, StatusURL string
	// TimeLimit ends the call after this long at most (Twilio's own
	// limit, a safety net under the agent's); 0 = Twilio's default.
	TimeLimit time.Duration
	// Ring is how long to let it ring; 0 = 30 s.
	Ring time.Duration
}

// CallInfo is a call as Twilio reports it.
type CallInfo struct {
	SID      string `json:"sid"`
	Status   string `json:"status"` // queued, ringing, in-progress, completed, busy, failed, no-answer, canceled
	To       string `json:"to"`
	From     string `json:"from"`
	Duration string `json:"duration"`
}

// Call places a call.
func (c *Client) Call(ctx context.Context, r CallRequest) (CallInfo, error) {
	form := url.Values{"To": {r.To}, "From": {r.From}, "Url": {r.URL}, "Method": {"POST"}}
	if r.StatusURL != "" {
		form.Set("StatusCallback", r.StatusURL)
		form.Set("StatusCallbackMethod", "POST")
		form["StatusCallbackEvent"] = []string{"initiated", "ringing", "answered", "completed"}
	}
	if r.TimeLimit > 0 {
		form.Set("TimeLimit", strconv.Itoa(int(r.TimeLimit/time.Second)))
	}
	ring := r.Ring
	if ring <= 0 {
		ring = 30 * time.Second
	}
	form.Set("Timeout", strconv.Itoa(int(ring/time.Second)))
	var info CallInfo
	err := c.do(ctx, http.MethodPost, "/Calls.json", form, &info)
	return info, err
}

// Hangup ends a call, ringing or answered (Status=completed). A call
// Twilio no longer knows is ended already.
func (c *Client) Hangup(ctx context.Context, callSID string) error {
	err := c.do(ctx, http.MethodPost, "/Calls/"+url.PathEscape(callSID)+".json", url.Values{"Status": {"completed"}}, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil
	}
	return err
}
