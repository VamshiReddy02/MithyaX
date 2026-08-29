package security_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/vamshireddy02/mithyax/gateway/internal/security"
)

// fakeResolver is a Resolver returning fixed, test-controlled DNS
// answers — the seam that makes "DNS -> private IP" / "DNS -> public
// IP" / "DNS rebinding" testable without depending on real DNS or the
// test machine's network.
type fakeResolver struct {
	answers map[string][]net.IPAddr
	err     error
}

func (f *fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if f.err != nil {
		return nil, f.err
	}
	addrs, ok := f.answers[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return addrs, nil
}

func ipAddrs(ips ...string) []net.IPAddr {
	addrs := make([]net.IPAddr, len(ips))
	for i, ip := range ips {
		addrs[i] = net.IPAddr{IP: net.ParseIP(ip)}
	}
	return addrs
}

func newValidator(answers map[string][]net.IPAddr) *security.Validator {
	return security.NewValidator(security.WithResolver(&fakeResolver{answers: answers}))
}

// --- scheme / parsing ---

func TestValidateURL_ValidHTTPS_Allowed(t *testing.T) {
	v := newValidator(map[string][]net.IPAddr{"example.com": ipAddrs("93.184.216.34")})
	if err := v.ValidateURL("https://example.com/video.mp4"); err != nil {
		t.Errorf("ValidateURL() error = %v, want nil", err)
	}
}

func TestValidateURL_ValidHTTP_Allowed(t *testing.T) {
	v := newValidator(map[string][]net.IPAddr{"example.com": ipAddrs("93.184.216.34")})
	if err := v.ValidateURL("http://example.com/video.mp4"); err != nil {
		t.Errorf("ValidateURL() error = %v, want nil", err)
	}
}

func TestValidateURL_Malformed_Rejected(t *testing.T) {
	tests := []string{
		"http://example.com/%zz",  // invalid percent-encoding
		"http://[::1",             // unterminated IPv6 literal
		"not a url at all",        // no scheme, no host
		"",                        // empty
		"   ",                     // blank
		"http://",                 // scheme with no host
	}
	v := newValidator(nil)
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if err := v.ValidateURL(raw); err == nil {
				t.Errorf("ValidateURL(%q) = nil, want an error", raw)
			}
		})
	}
}

func TestValidateURL_DisallowedSchemes_Rejected(t *testing.T) {
	tests := []string{
		"file:///etc/passwd",
		"ftp://example.com/video.mp4",
		"gopher://example.com/",
		"data://text/plain;base64,aGVsbG8=",
		"javascript://alert(1)",
	}
	v := newValidator(nil)
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			err := v.ValidateURL(raw)
			if !errors.Is(err, security.ErrDisallowedScheme) {
				t.Errorf("ValidateURL(%q) error = %v, want ErrDisallowedScheme", raw, err)
			}
		})
	}
}

func TestValidateURL_SchemeCaseInsensitive_Allowed(t *testing.T) {
	v := newValidator(map[string][]net.IPAddr{"example.com": ipAddrs("93.184.216.34")})
	if err := v.ValidateURL("HTTPS://example.com/video.mp4"); err != nil {
		t.Errorf("ValidateURL() error = %v, want nil (scheme match should be case-insensitive)", err)
	}
}

// --- literal IPv4 loopback / private / link-local / etc ---

func TestValidateURL_Localhost_Rejected(t *testing.T) {
	v := newValidator(nil)
	for _, raw := range []string{"http://localhost/", "http://LOCALHOST/", "http://localhost:8080/"} {
		if err := v.ValidateURL(raw); !errors.Is(err, security.ErrPrivateAddress) {
			t.Errorf("ValidateURL(%q) error = %v, want ErrPrivateAddress", raw, err)
		}
	}
}

func TestValidateURL_Loopback127_Rejected(t *testing.T) {
	v := newValidator(nil)
	if err := v.ValidateURL("http://127.0.0.1/"); !errors.Is(err, security.ErrPrivateAddress) {
		t.Errorf("ValidateURL() error = %v, want ErrPrivateAddress", err)
	}
}

func TestValidateURL_ExplicitPort_StillRejected(t *testing.T) {
	v := newValidator(nil)
	if err := v.ValidateURL("http://127.0.0.1:8080/"); !errors.Is(err, security.ErrPrivateAddress) {
		t.Errorf("ValidateURL() error = %v, want ErrPrivateAddress — a port must not hide the host", err)
	}
}

func TestValidateURL_Unspecified_Rejected(t *testing.T) {
	v := newValidator(nil)
	for _, raw := range []string{"http://0.0.0.0/", "http://[::]/"} {
		if err := v.ValidateURL(raw); !errors.Is(err, security.ErrPrivateAddress) {
			t.Errorf("ValidateURL(%q) error = %v, want ErrPrivateAddress", raw, err)
		}
	}
}

func TestValidateURL_PrivateIPv4Ranges_Rejected(t *testing.T) {
	tests := []string{
		"http://10.0.0.1/",
		"http://10.255.255.255/",
		"http://172.16.0.1/",
		"http://172.31.255.255/",
		"http://192.168.0.1/",
		"http://192.168.255.255/",
		"http://169.254.169.254/", // cloud metadata endpoint
		"http://169.254.0.1/",
	}
	v := newValidator(nil)
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if err := v.ValidateURL(raw); !errors.Is(err, security.ErrPrivateAddress) {
				t.Errorf("ValidateURL(%q) error = %v, want ErrPrivateAddress", raw, err)
			}
		})
	}
}

func TestValidateURL_MulticastIPv4_Rejected(t *testing.T) {
	v := newValidator(nil)
	if err := v.ValidateURL("http://224.0.0.1/"); !errors.Is(err, security.ErrPrivateAddress) {
		t.Errorf("ValidateURL() error = %v, want ErrPrivateAddress", err)
	}
}

// --- IPv6 ---

func TestValidateURL_IPv6Loopback_Rejected(t *testing.T) {
	v := newValidator(nil)
	if err := v.ValidateURL("http://[::1]/"); !errors.Is(err, security.ErrPrivateAddress) {
		t.Errorf("ValidateURL() error = %v, want ErrPrivateAddress", err)
	}
}

func TestValidateURL_IPv6PrivateAndLinkLocal_Rejected(t *testing.T) {
	tests := []string{
		"http://[fc00::1]/",  // unique local (RFC 4193)
		"http://[fd12::1]/",  // unique local
		"http://[fe80::1]/",  // link-local
		"http://[ff02::1]/",  // link-local multicast
	}
	v := newValidator(nil)
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if err := v.ValidateURL(raw); !errors.Is(err, security.ErrPrivateAddress) {
				t.Errorf("ValidateURL(%q) error = %v, want ErrPrivateAddress", raw, err)
			}
		})
	}
}

func TestValidateURL_IPv6PublicAddress_Allowed(t *testing.T) {
	v := newValidator(nil)
	// 2001:4860:4860::8888 is a real public address (Google DNS) — used
	// here as a literal IP, no DNS lookup involved.
	if err := v.ValidateURL("http://[2001:4860:4860::8888]/"); err != nil {
		t.Errorf("ValidateURL() error = %v, want nil", err)
	}
}

// --- encoded / alternate IPv4 forms ---

func TestValidateURL_AlternateIPv4Forms_Rejected(t *testing.T) {
	tests := []string{
		"http://2130706433/",         // 127.0.0.1 as a plain 32-bit decimal integer
		"http://0x7f000001/",         // 127.0.0.1 as hex
		"http://0177.0.0.1/",         // 127.0.0.1 with an octal first octet
		"http://127.1/",              // shorthand: 127.0.0.1
		"http://0x7f.0.0.1/",         // mixed hex/decimal octets
		fmt.Sprintf("http://%d/", 0xA9FEA9FE), // 169.254.169.254 as decimal
	}
	v := newValidator(nil)
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if err := v.ValidateURL(raw); !errors.Is(err, security.ErrPrivateAddress) {
				t.Errorf("ValidateURL(%q) error = %v, want ErrPrivateAddress", raw, err)
			}
		})
	}
}

func TestValidateURL_IPv4MappedIPv6_Rejected(t *testing.T) {
	v := newValidator(nil)
	if err := v.ValidateURL("http://[::ffff:127.0.0.1]/"); !errors.Is(err, security.ErrPrivateAddress) {
		t.Errorf("ValidateURL() error = %v, want ErrPrivateAddress — an IPv4-mapped IPv6 loopback must still be caught", err)
	}
}

// --- DNS-dependent cases (the actual SSRF-via-hostname scenarios) ---

func TestValidateURL_DNSResolvesToPrivateIP_Rejected(t *testing.T) {
	v := newValidator(map[string][]net.IPAddr{
		"evil.example": ipAddrs("127.0.0.1"),
	})
	if err := v.ValidateURL("https://evil.example/video.mp4"); !errors.Is(err, security.ErrPrivateAddress) {
		t.Errorf("ValidateURL() error = %v, want ErrPrivateAddress", err)
	}
}

func TestValidateURL_DNSResolvesToPublicIP_Allowed(t *testing.T) {
	v := newValidator(map[string][]net.IPAddr{
		"cdn.example": ipAddrs("93.184.216.34"),
	})
	if err := v.ValidateURL("https://cdn.example/video.mp4"); err != nil {
		t.Errorf("ValidateURL() error = %v, want nil", err)
	}
}

// TestValidateURL_DNSRebinding_OneOfMultipleAnswersIsPrivate proves the
// core DNS-rebinding defense: a name that resolves to several
// addresses is rejected if even one of them is private, not just the
// first one checked or the one a particular client happens to connect
// to.
func TestValidateURL_DNSRebinding_OneOfMultipleAnswersIsPrivate(t *testing.T) {
	v := newValidator(map[string][]net.IPAddr{
		"evil.example": ipAddrs("93.184.216.34", "127.0.0.1"),
	})
	if err := v.ValidateURL("https://evil.example/video.mp4"); !errors.Is(err, security.ErrPrivateAddress) {
		t.Errorf("ValidateURL() error = %v, want ErrPrivateAddress — one private answer among several must still reject", err)
	}
}

func TestValidateURL_UnresolvableHost_Rejected(t *testing.T) {
	v := newValidator(nil) // no answers configured -> fakeResolver returns NXDOMAIN
	if err := v.ValidateURL("https://does-not-exist.example/video.mp4"); !errors.Is(err, security.ErrUnresolvableHost) {
		t.Errorf("ValidateURL() error = %v, want ErrUnresolvableHost", err)
	}
}

// --- URL tricks ---

// TestValidateURL_UserInfoTrick_UsesRealHost proves a userinfo-prefixed
// URL is judged by its actual host, not by a trusted-looking name
// sitting in front of the '@' — a classic SSRF/phishing confusion
// where naive string matching checks the wrong part of the URL.
func TestValidateURL_UserInfoTrick_UsesRealHost(t *testing.T) {
	v := newValidator(nil)
	if err := v.ValidateURL("http://trusted.example@127.0.0.1/"); !errors.Is(err, security.ErrPrivateAddress) {
		t.Errorf("ValidateURL() error = %v, want ErrPrivateAddress — the real host is 127.0.0.1, not trusted.example", err)
	}
}

// TestValidateURL_TrailingDot_StillRecognizedAsPrivate proves a
// trailing-dot FQDN form of a private literal IP is normalized the
// same way a real resolver would treat it, not accidentally let
// through as an unrecognized hostname.
func TestValidateURL_TrailingDot_StillRecognizedAsPrivate(t *testing.T) {
	v := newValidator(nil)
	if err := v.ValidateURL("http://127.0.0.1./"); !errors.Is(err, security.ErrPrivateAddress) {
		t.Errorf("ValidateURL() error = %v, want ErrPrivateAddress", err)
	}
}

func TestValidateURL_TrailingDotHostname_ResolvesNormally(t *testing.T) {
	v := newValidator(map[string][]net.IPAddr{
		"cdn.example": ipAddrs("93.184.216.34"),
	})
	if err := v.ValidateURL("https://cdn.example./video.mp4"); err != nil {
		t.Errorf("ValidateURL() error = %v, want nil — a trailing dot on an otherwise-public FQDN must still resolve", err)
	}
}

// TestValidateURL_JudgesOnlyTheGivenURL_NotAnyRedirectTarget documents
// a boundary this validator deliberately doesn't cross (per the
// ticket's own closing note): it makes no network request of its own,
// so it validates exactly the URL string it's given — never anything
// a server behind that URL might later redirect a real fetch to. A
// resolver that would fail the test outright if ValidateURL ever
// tried to look up anything beyond the one hostname in the URL proves
// this concretely. Making a real HTTP fetch safe means re-validating
// every redirect Location at the fetcher itself, which is a later
// phase's job (see the package doc).
func TestValidateURL_JudgesOnlyTheGivenURL_NotAnyRedirectTarget(t *testing.T) {
	lookups := map[string][]net.IPAddr{"cdn.example": ipAddrs("93.184.216.34")}
	resolver := &recordingResolver{fakeResolver: fakeResolver{answers: lookups}}
	v := security.NewValidator(security.WithResolver(resolver))

	if err := v.ValidateURL("https://cdn.example/video.mp4"); err != nil {
		t.Fatalf("ValidateURL() error = %v, want nil", err)
	}
	if got := resolver.hosts; len(got) != 1 || got[0] != "cdn.example" {
		t.Errorf("resolver was asked to look up %v, want exactly [cdn.example] — a redirect target must never be dereferenced here", got)
	}
}

// recordingResolver wraps fakeResolver to record every hostname it's
// asked to look up, for asserting ValidateURL never resolves anything
// beyond the URL it was actually given.
type recordingResolver struct {
	fakeResolver
	hosts []string
}

func (r *recordingResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r.hosts = append(r.hosts, host)
	return r.fakeResolver.LookupIPAddr(ctx, host)
}
