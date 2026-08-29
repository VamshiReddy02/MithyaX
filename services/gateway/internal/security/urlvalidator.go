// Package security guards against server-side request forgery (SSRF,
// Phase 7.7.4): MithyaX's async analysis API accepts video_url/
// audio_url from clients, and a worker eventually makes a real HTTP
// request to whatever URL it's handed. Without validation, a
// malicious client could point that request at the gateway's own
// internal infrastructure (http://127.0.0.1:8080/..., a database port,
// a cloud metadata endpoint at 169.254.169.254, ...) and use the
// worker as a proxy into a network it should never be able to reach.
//
// This package only builds the reusable validator — see
// Validator.ValidateURL's own doc for the checks it performs. Wiring
// it into POST /api/v1/analysis and the worker's fetch path (both
// call sites need it independently — see that doc for why) is a later
// phase's job.
package security

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidURL means rawURL itself couldn't be parsed, or was missing
// a piece (scheme, host) every valid URL needs.
var ErrInvalidURL = errors.New("invalid URL")

// ErrDisallowedScheme means rawURL's scheme isn't http or https —
// file://, ftp://, gopher://, data://, javascript://, and everything
// else are all rejected the same way: an HTTP fetcher only ever needs
// to speak HTTP.
var ErrDisallowedScheme = errors.New("disallowed URL scheme")

// ErrUnresolvableHost means the hostname doesn't resolve to any IP
// address at all.
var ErrUnresolvableHost = errors.New("hostname does not resolve to any address")

// ErrPrivateAddress means rawURL's host — or, for a DNS name, at least
// one of the addresses it resolves to — is not a publicly routable
// address: loopback, a private/link-local/multicast range, or
// unspecified (0.0.0.0 / ::). See isPublicIP for the exact rules.
var ErrPrivateAddress = errors.New("URL resolves to a non-public address")

// allowedSchemes is the entire scheme allowlist. Deliberately an
// allowlist, not a blocklist of file/ftp/gopher/data/javascript/etc —
// a blocklist only ever protects against the schemes someone thought
// to name.
var allowedSchemes = map[string]bool{
	"http":  true,
	"https": true,
}

// defaultResolveTimeout bounds how long ValidateURL (the context-free
// convenience form) will wait on DNS before giving up — a hostname
// that resolves very slowly, or not at all, must not hang whatever
// request path is calling this synchronously.
const defaultResolveTimeout = 5 * time.Second

// Resolver resolves a hostname to its IP addresses. Exists as a seam
// so tests can inject fixed DNS answers instead of depending on real
// resolution or on literal-IP-only test cases; *net.Resolver (in
// particular net.DefaultResolver) already satisfies it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Validator checks whether a URL is safe to let an HTTP fetcher (a
// worker, or the API handler validating before ever enqueuing a job)
// actually request. One Validator can be shared and reused freely —
// it holds no per-request state.
type Validator struct {
	resolver Resolver
}

// Option configures a Validator built by NewValidator.
type Option func(*Validator)

// WithResolver overrides the default resolver (net.DefaultResolver) —
// used by tests to supply fixed DNS answers without touching the
// network.
func WithResolver(r Resolver) Option {
	return func(v *Validator) { v.resolver = r }
}

// NewValidator builds a Validator. With no options it resolves
// hostnames using the process's normal DNS configuration.
func NewValidator(opts ...Option) *Validator {
	v := &Validator{resolver: net.DefaultResolver}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// ValidateURL is the context-free convenience form of ValidateURLContext,
// bounded by defaultResolveTimeout so a slow or hanging DNS lookup
// can't block its caller indefinitely.
func (v *Validator) ValidateURL(rawURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultResolveTimeout)
	defer cancel()
	return v.ValidateURLContext(ctx, rawURL)
}

// ValidateURLContext reports whether rawURL is safe to fetch:
//
//	Parse URL -> validate scheme -> validate hostname -> DNS resolution -> check every IP -> allowed?
//
// A literal IP host is checked directly. A DNS name is resolved via
// the configured Resolver, and every address it comes back with — not
// just the first — must be publicly routable, which is what defeats
// DNS rebinding: a name can resolve to more than one address, and an
// attacker only needs one of them to be internal.
//
// This check is only as current as the DNS answer used to produce it.
// A name validated here as public can resolve differently by the time
// something actually fetches it — which is exactly why this same
// Validator must be called again immediately before the fetch itself
// (see the package doc), not just once up front at the API boundary:
// a job can sit in a queue for hours between the two.
func (v *Validator) ValidateURLContext(ctx context.Context, rawURL string) error {
	parsed, err := parseURL(rawURL)
	if err != nil {
		return err
	}

	host := strings.TrimSuffix(parsed.Hostname(), ".")
	if host == "" {
		return fmt.Errorf("%w: no host", ErrInvalidURL)
	}

	if ip := literalIP(host); ip != nil {
		if !isPublicIP(ip) {
			return fmt.Errorf("%w: %s", ErrPrivateAddress, ip)
		}
		return nil
	}

	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("%w: localhost", ErrPrivateAddress)
	}

	addrs, err := v.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrUnresolvableHost, host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: %s", ErrUnresolvableHost, host)
	}
	for _, addr := range addrs {
		if !isPublicIP(addr.IP) {
			return fmt.Errorf("%w: %s resolves to %s", ErrPrivateAddress, host, addr.IP)
		}
	}
	return nil
}

// parseURL parses rawURL and validates everything checkable without
// touching the network: it must actually parse, carry an allowed
// scheme, and have a host.
func parseURL(rawURL string) (*url.URL, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, fmt.Errorf("%w: empty URL", ErrInvalidURL)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	if !allowedSchemes[strings.ToLower(parsed.Scheme)] {
		return nil, fmt.Errorf("%w: %q", ErrDisallowedScheme, parsed.Scheme)
	}

	if parsed.Host == "" {
		return nil, fmt.Errorf("%w: no host", ErrInvalidURL)
	}

	return parsed, nil
}

// literalIP reports whether host is itself an IP address — as
// standard dotted-decimal/colon-hex (net.ParseIP), or as one of the
// alternate numeric forms parseAlternateIPv4 understands — returning
// it if so, or nil if host looks like a genuine DNS name.
func literalIP(host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	if ip, ok := parseAlternateIPv4(host); ok {
		return ip
	}
	return nil
}

// parseAlternateIPv4 recognizes IPv4 forms net.ParseIP does not: a
// single 32-bit integer, 2- or 3-part shorthand (the last part
// absorbing the remaining bits), and octal (leading 0) or hexadecimal
// (leading 0x) components in any of them — classic inet_aton syntax.
// This isn't a hypothetical: "http://0x7f000001/", "http://2130706433/",
// and "http://127.1/" all mean http://127.0.0.1/ to plenty of real
// HTTP clients' own resolvers, which is exactly what makes them a live
// SSRF bypass against any validator that only ever recognizes
// dotted-decimal — net.ParseIP among them.
func parseAlternateIPv4(host string) (net.IP, bool) {
	parts := strings.Split(host, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return nil, false
	}

	nums := make([]uint64, len(parts))
	for i, p := range parts {
		if p == "" {
			return nil, false
		}
		n, err := strconv.ParseUint(p, 0, 64) // base 0: auto-detects 0x/0 prefixes
		if err != nil {
			return nil, false
		}
		nums[i] = n
	}

	// Every part but the last must fit in a single byte; the last one
	// absorbs however many bits the omitted parts would have carried.
	for _, n := range nums[:len(nums)-1] {
		if n > 0xff {
			return nil, false
		}
	}
	lastBits := uint(8 * (5 - len(nums)))
	last := nums[len(nums)-1]
	if lastBits < 64 && last >= uint64(1)<<lastBits {
		return nil, false
	}

	var b [4]byte
	for i := 0; i < len(nums)-1; i++ {
		b[i] = byte(nums[i])
	}
	for i, shift := len(nums)-1, lastBits; i < 4; i, shift = i+1, shift-8 {
		b[i] = byte(last >> (shift - 8))
	}
	return net.IPv4(b[0], b[1], b[2], b[3]), true
}

// isPublicIP reports whether ip is safe to let an HTTP fetcher
// connect to. ip.To4() is applied first so an IPv4-mapped IPv6 address
// (e.g. ::ffff:127.0.0.1 — itself another SSRF bypass technique
// against a checker that only inspects the 16-byte form) is judged by
// its real IPv4 identity rather than slipping past IPv4-shaped checks.
//
// IsPrivate covers RFC 1918 (10/8, 172.16/12, 192.168/16) and RFC 4193
// IPv6 unique local addresses in one call; IsLinkLocalUnicast covers
// 169.254.0.0/16 (notably the cloud metadata endpoint at
// 169.254.169.254) and fe80::/10; the rest are self-explanatory.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	switch {
	case ip.IsLoopback(),
		ip.IsPrivate(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(),
		ip.IsMulticast(),
		ip.IsUnspecified():
		return false
	}
	return true
}
