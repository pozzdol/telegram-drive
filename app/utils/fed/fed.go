// Package fed is the server-to-server part of federation (docs/federation.md): this server's
// key, discovery of other servers, signing and checking requests (RFC 9421 HTTP Message
// Signatures with Ed25519), and an HTTP client that never calls private addresses.
package fed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	Software   = "telegram-drive"
	Version    = "1"
	maxSkew    = 5 * time.Minute
	cacheFor   = time.Hour
	maxDocSize = 16 << 10
)

// Node is this server as seen by others.
type Node struct {
	Host   string // address host: the UI's domain, as in fikri@drive.example.org
	API    string // public base URL of this API, e.g. https://drive-api.example.org
	KeyID  string
	Key    ed25519.PrivateKey
	Scheme string // "https"; tests use "http"

	AllowPrivate bool     // LAN setups and tests: let the client reach private addresses
	Allowed      []string // if set, only these hosts may federate with this server

	JSON   *http.Client // short requests
	Stream *http.Client // proxied file bodies: no overall timeout, the request context ends them

	mu    sync.Mutex
	peers map[string]peer      // host -> discovery result
	seen  map[string]time.Time // signatures already used, against replays
}

type peer struct {
	api   string
	keyID string
	key   ed25519.PublicKey
	at    time.Time
}

// New loads (or on first start creates) the key in keyPath.
func New(host, api, keyPath string, allowPrivate bool, allowed []string) (*Node, error) {
	n := &Node{Host: strings.ToLower(host), API: strings.TrimRight(api, "/"), Scheme: "https", AllowPrivate: allowPrivate,
		Allowed: allowed, peers: map[string]peer{}, seen: map[string]time.Time{}}
	seed, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		seed = make([]byte, ed25519.SeedSize)
		rand.Read(seed)
		if err := os.WriteFile(keyPath, seed, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: not an Ed25519 seed", keyPath)
	}
	n.Key = ed25519.NewKeyFromSeed(seed)
	n.KeyID = n.API + "/fed/v1/server#key"
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: n.checkAddr}
	tr := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second, MaxIdleConnsPerHost: 4}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	n.JSON = &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: noRedirect}
	n.Stream = &http.Client{Transport: tr, CheckRedirect: noRedirect}
	return n, nil
}

// checkAddr refuses connections to loopback, private, link-local and other internal addresses,
// after DNS resolution, so another server cannot make this one call into its own network (SSRF).
func (n *Node) checkAddr(network, address string, _ syscall.RawConn) error {
	if n.AllowPrivate {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return fmt.Errorf("federation: refusing to connect to %s", host)
	}
	return nil
}

// AllowedHost reports whether host may federate with this server (FED_ALLOWED_HOSTS).
func (n *Node) AllowedHost(host string) bool {
	if len(n.Allowed) == 0 {
		return true
	}
	for _, h := range n.Allowed {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// ParseAddress splits "user@host". The host is lowercased; a port is allowed (tests, LAN).
func ParseAddress(a string) (user, host string, ok bool) {
	user, host, ok = strings.Cut(strings.TrimSpace(a), "@")
	host = strings.ToLower(host)
	if !ok || user == "" || host == "" || strings.ContainsAny(host, "/?#@ \\") || strings.Contains(user, "@") {
		return "", "", false
	}
	return user, host, true
}

// Address is user's federated address on this server.
func (n *Node) Address(user string) string { return user + "@" + n.Host }

// ---- discovery ----

// ServerDoc is what GET /fed/v1/server (and /.well-known/telegram-drive) answers.
func (n *Node) ServerDoc() map[string]any {
	return map[string]any{
		"software": Software,
		"versions": []string{Version},
		"host":     n.Host,
		"api":      n.API,
		"key":      map[string]string{"id": n.KeyID, "alg": "ed25519", "public": base64.StdEncoding.EncodeToString(n.Key.Public().(ed25519.PublicKey))},
	}
}

func (n *Node) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	res, err := n.JSON.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", u, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, maxDocSize)).Decode(v)
}

// Resolve finds the API and key of the server behind an address host. The host's
// /.well-known/telegram-drive names the API; the API's /fed/v1/server holds the key.
func (n *Node) Resolve(ctx context.Context, host string) (api, keyID string, key ed25519.PublicKey, err error) {
	host = strings.ToLower(host)
	if !n.AllowedHost(host) {
		return "", "", nil, fmt.Errorf("federation with %s is not allowed on this server", host)
	}
	n.mu.Lock()
	p, ok := n.peers[host]
	n.mu.Unlock()
	if ok && time.Since(p.at) < cacheFor {
		return p.api, p.keyID, p.key, nil
	}
	var wk struct {
		Software string `json:"software"`
		API      string `json:"api"`
	}
	if err := n.getJSON(ctx, n.Scheme+"://"+host+"/.well-known/telegram-drive", &wk); err != nil {
		return "", "", nil, fmt.Errorf("%s is not a Telegram Drive server (%v)", host, err)
	}
	if wk.Software != Software {
		return "", "", nil, fmt.Errorf("%s is not a Telegram Drive server", host)
	}
	if u, err := url.Parse(wk.API); err != nil || (u.Scheme != n.Scheme) || u.Host == "" {
		return "", "", nil, fmt.Errorf("%s: bad API address", host)
	}
	var doc struct {
		Software string   `json:"software"`
		Versions []string `json:"versions"`
		API      string   `json:"api"`
		Key      struct {
			ID     string `json:"id"`
			Alg    string `json:"alg"`
			Public string `json:"public"`
		} `json:"key"`
	}
	api = strings.TrimRight(wk.API, "/")
	if err := n.getJSON(ctx, api+"/fed/v1/server", &doc); err != nil {
		return "", "", nil, fmt.Errorf("%s: %v", host, err)
	}
	pub, _ := base64.StdEncoding.DecodeString(doc.Key.Public)
	compatible := false
	for _, v := range doc.Versions {
		compatible = compatible || v == Version
	}
	if doc.Software != Software || !compatible || doc.Key.Alg != "ed25519" || len(pub) != ed25519.PublicKeySize ||
		strings.TrimRight(doc.API, "/") != api {
		return "", "", nil, fmt.Errorf("%s: incompatible server", host)
	}
	p = peer{api: api, keyID: doc.Key.ID, key: ed25519.PublicKey(pub), at: time.Now()}
	n.mu.Lock()
	n.peers[host] = p
	n.mu.Unlock()
	return p.api, p.keyID, p.key, nil
}

// ---- signing (RFC 9421, one fixed profile) ----

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
}

func sigBase(method, target, contentDigest, params string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\"@method\": %s\n\"@target-uri\": %s\n", method, target)
	if contentDigest != "" {
		fmt.Fprintf(&b, "\"content-digest\": %s\n", contentDigest)
	}
	fmt.Fprintf(&b, "\"@signature-params\": %s", params)
	return b.String()
}

// Sign adds Content-Digest (when there is a body), Signature-Input and Signature to req.
// body must be exactly what req sends.
func (n *Node) Sign(req *http.Request, body []byte) {
	components := `("@method" "@target-uri")`
	cd := ""
	if body != nil {
		cd = digest(body)
		req.Header.Set("Content-Digest", cd)
		components = `("@method" "@target-uri" "content-digest")`
	}
	params := fmt.Sprintf(`%s;created=%d;keyid="%s";alg="ed25519"`, components, time.Now().Unix(), n.KeyID)
	sig := ed25519.Sign(n.Key, []byte(sigBase(req.Method, req.URL.String(), cd, params)))
	req.Header.Set("Signature-Input", "sig1="+params)
	req.Header.Set("Signature", "sig1=:"+base64.StdEncoding.EncodeToString(sig)+":")
}

var ErrSignature = errors.New("federation: bad or missing signature")

// Verify checks that r was signed by the server behind host. body is the request body already
// read (nil when there is none). The target URI is rebuilt from this server's public API URL.
func (n *Node) Verify(ctx context.Context, r *http.Request, body []byte, host string) error {
	in, ok := strings.CutPrefix(r.Header.Get("Signature-Input"), "sig1=")
	sv, ok2 := strings.CutPrefix(r.Header.Get("Signature"), "sig1=:")
	if !ok || !ok2 || !strings.HasSuffix(sv, ":") {
		return ErrSignature
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(sv, ":"))
	if err != nil {
		return ErrSignature
	}
	components, rest, _ := strings.Cut(in, ";")
	cd := ""
	switch components {
	case `("@method" "@target-uri")`:
		if len(body) > 0 {
			return ErrSignature // a body must be covered
		}
	case `("@method" "@target-uri" "content-digest")`:
		cd = r.Header.Get("Content-Digest")
		if cd != digest(body) {
			return ErrSignature
		}
	default:
		return ErrSignature
	}
	var created int64
	var keyID string
	for _, p := range strings.Split(rest, ";") {
		k, v, _ := strings.Cut(p, "=")
		switch k {
		case "created":
			created, _ = strconv.ParseInt(v, 10, 64)
		case "keyid":
			keyID = strings.Trim(v, `"`)
		}
	}
	if d := time.Since(time.Unix(created, 0)); d > maxSkew || d < -maxSkew {
		return ErrSignature
	}
	_, wantID, key, err := n.Resolve(ctx, host)
	if err != nil {
		return err
	}
	if keyID != wantID {
		// ponytail: no key rotation yet (docs/federation.md section 4); a new key is refused
		// until the cached discovery expires.
		return ErrSignature
	}
	if !ed25519.Verify(key, []byte(sigBase(r.Method, n.API+r.URL.RequestURI(), cd, in)), sig) {
		return ErrSignature
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for s, at := range n.seen {
		if time.Since(at) > 2*maxSkew {
			delete(n.seen, s)
		}
	}
	if _, dup := n.seen[sv]; dup {
		return ErrSignature
	}
	n.seen[sv] = time.Now()
	return nil
}

// ---- calls to other servers ----

// Call sends a signed JSON request and decodes a JSON answer into out (when not nil).
// headers are extra headers such as Authorization.
func (n *Node) Call(ctx context.Context, method, u string, in, out any, headers map[string]string) (int, error) {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Body, req.ContentLength = http.NoBody, 0
		body = nil
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	n.Sign(req, body)
	res, err := n.JSON.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(res.Body, maxDocSize)).Decode(&e)
		return res.StatusCode, fmt.Errorf("%s: HTTP %d %s", u, res.StatusCode, e.Error)
	}
	if out != nil {
		return res.StatusCode, json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
	}
	return res.StatusCode, nil
}
