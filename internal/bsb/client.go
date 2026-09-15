// Package bsb implements the HTTP client for the BSB OPAC+ discovery backend
// (Ex Libris Primo VE, "suprima") hosted at
// https://opacplus.bsb-muenchen.de/primaws/rest.
//
// Public search (research) works anonymously via a guest JWT.
// Authenticated calls (inspect with request options, borrow, account data)
// need a user JWT obtained through the PDS login form
// (/primaws/suprimaExtLogin -> /primaws/pdsHandleLogin -> loginId ->
// /primaws/rest/pub/loginJwtCache/<loginId>).
package bsb

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// BaseURL is the root of the public Primo REST API.
	BaseURL = "https://opacplus.bsb-muenchen.de"
	// Institution is the BSB Alma institution code.
	Institution = "49BVB_BSB"
	// DefaultView is the default discovery view (OPACplus catalog).
	DefaultView = "49BVB_BSB:VU1"
	// DefaultScope searches the local catalog plus the central index.
	DefaultScope = "MyInst_and_CI"
	// DefaultTab is the discovery tab used by the web UI.
	DefaultTab = "Everything"
)

// userAgent identifies this CLI tool. The server behaves like a browser
// only if a browser UA is sent, so we mimic Firefox (as in the HAR file).
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:154.0) Gecko/20100101 Firefox/154.0"

// Client is a BSB OPAC+ API client. It owns an HTTP client with a cookie jar
// (the backend sets JSESSIONID / urm_* session cookies) and optionally a
// JWT used as `Authorization: Bearer <jwt>` for authenticated endpoints.
type Client struct {
	HTTP       *http.Client
	View       string
	Lang       string
	JWT        string
	cookieFile string
}

// NewClient creates a client with the given view/lang. Pass jwt="" for
// anonymous (guest) mode.
func NewClient(view, lang, jwt string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		HTTP: &http.Client{Timeout: 30 * time.Second, Jar: jar},
		View: view,
		Lang: lang,
		JWT:  jwt,
	}
}

func (c *Client) get(path string, query url.Values, out any) error {
	return c.do("GET", path, query, nil, out)
}

func (c *Client) do(method, path string, query url.Values, body io.Reader, out any) error {
	u := BaseURL + path
	if query == nil {
		query = url.Values{}
	}
	if _, ok := query["vid"]; !ok {
		query.Set("vid", c.View)
	}
	if _, ok := query["lang"]; !ok {
		query.Set("lang", c.Lang)
	}
	if enc := query.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if c.JWT != "" {
		req.Header.Set("Authorization", "Bearer "+c.JWT)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("request %s failed with HTTP %d: %s", path, resp.StatusCode, truncate(string(raw), 300))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s: %w (body: %s)", path, err, truncate(string(raw), 300))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// GuestJWT fetches an anonymous guest JWT for the configured view.
// Public search works without credentials using this token (or even none).
func (c *Client) GuestJWT() (string, error) {
	q := url.Values{
		"viewId":  {c.View},
		"lang":    {c.Lang},
		"isGuest": {"true"},
		"vid":     {c.View}, // do() adds vid/lang anyway; harmless duplicate guard below
	}
	q.Del("vid")
	q.Del("lang")
	var token string
	u := BaseURL + "/primaws/rest/pub/institution/" + Institution + "/guestJwt?" + q.Encode()
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("guestJwt failed with HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, &token); err != nil {
		return "", fmt.Errorf("decode guestJwt: %w", err)
	}
	return token, nil
}

// JWTClaims holds the decoded (unverified) payload of a Primo JWT.
type JWTClaims struct {
	User        string `json:"user"`
	UserName    string `json:"userName"`
	Display     string `json:"displayName"`
	Group       string `json:"userGroup"`
	Institution string `json:"institution"`
	ViewID      string `json:"viewId"`
	Language    string `json:"language"`
	OnCampus    string `json:"onCampus"`
	SignedIn    string `json:"signedIn"`
	ExpiresAt   int64  `json:"exp"`
	IssuedAt    int64  `json:"iat"`
}

// DecodeJWT decodes the payload of a JWT without verifying the signature.
// The signature is verified server-side; the CLI only uses claims for display.
func DecodeJWT(token string) (*JWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT: expected 3 parts")
	}
	raw := parts[1]
	if m := len(raw) % 4; m != 0 {
		raw += strings.Repeat("=", 4-m)
	}
	b, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}
	var cl JWTClaims
	if err := json.Unmarshal(b, &cl); err != nil {
		return nil, fmt.Errorf("parse JWT payload: %w", err)
	}
	return &cl, nil
}

// Login performs a PDS username/password login (auth=local) and returns the
// user JWT. Flow:
//  1. GET /primaws/suprimaExtLogin?... -> login form + session cookies
//  2. POST /primaws/pdsHandleLogin (username, password, institute,
//     productCode, deep-link, debug) -> 302 to /primaws/postLogin
//  3. follow redirects -> landing URL contains ?loginId=<id>
//  4. GET /primaws/rest/pub/loginJwtCache/<loginId>?vid=... -> JWT (JSON string)
func (c *Client) Login(username, password string) (jwt string, loginID string, err error) {
	target := "https://opacplus.bsb-muenchen.de/discovery/search?vid=" + url.QueryEscape(c.View)
	loginURL := BaseURL + "/primaws/suprimaExtLogin?institution=" + Institution +
		"&lang=" + url.QueryEscape(c.Lang) +
		"&target-url=" + url.QueryEscape(target) +
		"&authenticationProfile=default&auth=local&view=" + url.QueryEscape(c.View) + "&isSilent=false"

	req, _ := http.NewRequest("GET", loginURL, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch login form: %w", err)
	}
	formHTML, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("fetch login form: HTTP %d", resp.StatusCode)
	}
	deepLink := ""
	if m := regexp.MustCompile(`name="deep-link"[^>]*>[\s\S]*?<option value="([^"]*)"`).FindStringSubmatch(string(formHTML)); m != nil {
		deepLink = m[1]
	}

	form := url.Values{
		"username":    {username},
		"password":    {password},
		"institute":   {Institution},
		"productCode": {"alma"},
		"deep-link":   {deepLink},
		"debug":       {""},
	}
	// Do not follow redirects automatically so we can observe postLogin.
	noredir := *c.HTTP
	noRedirClient := &noredir
	noRedirClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	post, _ := http.NewRequest("POST", BaseURL+"/primaws/pdsHandleLogin", strings.NewReader(form.Encode()))
	post.Header.Set("User-Agent", userAgent)
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Referer", loginURL)
	resp2, err := noRedirClient.Do(post)
	if err != nil {
		return "", "", fmt.Errorf("submit login: %w", err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 302 {
		return "", "", fmt.Errorf("login rejected: expected redirect after POST, got HTTP %d (bad username/password?)", resp2.StatusCode)
	}
	// Follow the redirect chain manually to capture the loginId.
	// postLogin responds 302 with an empty body and Go's cookiejar client
	// sometimes gets a 403 when re-GETting via client.Get (missing Referer);
	// walk the chain with explicit requests carrying Referer instead.
	next, _ := resp2.Location()
	referer := BaseURL + "/primaws/pdsHandleLogin"
	var finalURL string
	for i := 0; i < 10; i++ {
		r, err := http.NewRequest("GET", next.String(), nil)
		if err != nil {
			return "", "", fmt.Errorf("follow postLogin: %w", err)
		}
		r.Header.Set("User-Agent", userAgent)
		r.Header.Set("Referer", referer)
		resp, err := c.HTTP.Do(r)
		if err != nil {
			return "", "", fmt.Errorf("follow postLogin: %w", err)
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		finalURL = resp.Request.URL.String()
		nextLoc := resp.Header.Get("Location")
		resp.Body.Close()
		referer = finalURL
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || nextLoc == "" {
			if !strings.Contains(finalURL, "/primaws/") {
				break
			}
			if nextLoc == "" {
				break
			}
		}
		base, _ := url.Parse(finalURL)
		rel, err := url.Parse(nextLoc)
		if err != nil {
			return "", "", fmt.Errorf("follow postLogin: bad Location %q", nextLoc)
		}
		next = base.ResolveReference(rel)
	}
	m := regexp.MustCompile(`[?&]loginId=([^&]+)`).FindStringSubmatch(finalURL)
	if m == nil {
		return "", "", fmt.Errorf("login failed: no loginId in landing URL (bad username/password?)")
	}
	loginID = m[1]

	var token string
	if err := c.get("/primaws/rest/pub/loginJwtCache/"+loginID, url.Values{}, &token); err != nil {
		return "", "", fmt.Errorf("exchange loginId for JWT: %w", err)
	}
	if token == "" {
		return "", "", fmt.Errorf("exchange loginId for JWT: empty token")
	}
	return token, loginID, nil
}
