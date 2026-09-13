package acquire

// The redirect policy is tested from inside the package, against checkRedirect
// itself, because two of its rules cannot be reached from outside it. An https
// server needs a certificate this reader's client would have to be told to
// trust, and there is deliberately no way to tell it that; and the hop cap
// needs a chain longer than the cap, which is a slow way to assert an integer.
// Everything the policy can be driven to from a real server is exercised in
// response_test.go.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// hops builds the via chain the http client passes to CheckRedirect: the
// requests already made, oldest first.
func hops(t *testing.T, urls ...string) []*http.Request {
	t.Helper()
	var out []*http.Request
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parsing %q: %v", raw, err)
		}
		out = append(out, &http.Request{URL: u})
	}
	return out
}

// TestCheckRedirect_FollowsOnlyWhatTheCallerWouldHaveChosen states the policy
// as a table.
//
// Go's default is ten redirects to any host over any scheme. Each row here is
// a thing that default permits and this reader does not, or a thing that is
// ordinary and has to keep working.
func TestCheckRedirect_FollowsOnlyWhatTheCallerWouldHaveChosen(t *testing.T) {
	cases := []struct {
		name string
		via  []string
		to   string
		want string // a phrase the refusal must contain; "" means it is allowed
	}{
		{
			name: "another path on the same host",
			via:  []string{"https://tiles.example/archive.pmtiles"},
			to:   "https://tiles.example/builds/2026-09-12.pmtiles",
		},
		{
			name: "an upgrade from http to https on the same host",
			via:  []string{"http://tiles.example/archive.pmtiles"},
			to:   "https://tiles.example/archive.pmtiles",
		},
		{
			name: "a downgrade from https to http",
			via:  []string{"https://tiles.example/archive.pmtiles"},
			to:   "http://tiles.example/archive.pmtiles",
			want: "refusing to downgrade",
		},
		{
			name: "a different host",
			via:  []string{"https://tiles.example/archive.pmtiles"},
			to:   "https://cdn.example/archive.pmtiles",
			want: "a host you did not name",
		},
		{
			name: "a subdomain of the same host, which is still another host",
			via:  []string{"https://tiles.example/archive.pmtiles"},
			to:   "https://www.tiles.example/archive.pmtiles",
			want: "a host you did not name",
		},
		{
			name: "the same host on another port",
			via:  []string{"https://tiles.example/archive.pmtiles"},
			to:   "https://tiles.example:8443/archive.pmtiles",
			want: "a host you did not name",
		},
		{
			name: "a host reachable only from this machine",
			via:  []string{"https://tiles.example/archive.pmtiles"},
			to:   "http://169.254.169.254/latest/meta-data/",
			want: "a host you did not name",
		},
		{
			name: "a scheme that is not http at all",
			via:  []string{"https://tiles.example/archive.pmtiles"},
			to:   "file:///etc/passwd",
			want: "scheme",
		},
		{
			name: "the same host, differently capitalised",
			via:  []string{"https://Tiles.Example/archive.pmtiles"},
			to:   "https://tiles.example/archive.pmtiles",
		},
		{
			name: "one hop under the cap",
			via: []string{
				"https://tiles.example/0", "https://tiles.example/1", "https://tiles.example/2",
				"https://tiles.example/3", "https://tiles.example/4",
			},
			to: "https://tiles.example/5",
		},
		{
			name: "one hop over the cap",
			via: []string{
				"https://tiles.example/0", "https://tiles.example/1", "https://tiles.example/2",
				"https://tiles.example/3", "https://tiles.example/4", "https://tiles.example/5",
			},
			to:   "https://tiles.example/6",
			want: "redirected more than",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			via := hops(t, c.via...)
			next := hops(t, c.to)[0]
			err := checkRedirect(next, via)
			if c.want == "" {
				if err != nil {
					t.Fatalf("checkRedirect refused an ordinary redirect: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkRedirect allowed a redirect to %s", c.to)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error is %q; it should say %q", err, c.want)
			}
		})
	}
}

// TestCheckRedirect_RefusalDoesNotRepeatACredential: the refusal names the
// source and the destination, and both come from URLs that may carry one.
func TestCheckRedirect_RefusalDoesNotRepeatACredential(t *testing.T) {
	via := hops(t, "https://someuser:hunter2@tiles.example/archive.pmtiles?X-Amz-Signature=abcdef")
	next := hops(t, "https://other:secret@cdn.example/archive.pmtiles?token=xyzzy")[0]
	err := checkRedirect(next, via)
	if err == nil {
		t.Fatal("checkRedirect allowed a cross-host redirect")
	}
	for _, secret := range []string{"hunter2", "someuser", "abcdef", "secret", "xyzzy"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the refusal leaks %q:\n%s", secret, err)
		}
	}
}

// TestRedactURL_KeepsTheArchiveIdentifiableAndTheSecretOut pins what a printed
// URL looks like.
//
// The userinfo becomes "redacted" rather than disappearing, and the query
// becomes "redacted" rather than being dropped, because the fact that a
// credential was in play is worth knowing when working out why a host said no.
// What is lost is a benign query string, which is the price of not keeping a
// list of which parameters are secret.
func TestRedactURL_KeepsTheArchiveIdentifiableAndTheSecretOut(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://tiles.example/archive.pmtiles", "https://tiles.example/archive.pmtiles"},
		{"https://user:pass@tiles.example/a.pmtiles", "https://redacted@tiles.example/a.pmtiles"},
		{"https://user@tiles.example/a.pmtiles", "https://redacted@tiles.example/a.pmtiles"},
		{"https://tiles.example/a.pmtiles?X-Amz-Signature=abcdef", "https://tiles.example/a.pmtiles?redacted"},
		{"https://tiles.example/a.pmtiles#note", "https://tiles.example/a.pmtiles"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.in)
		if err != nil {
			t.Fatalf("parsing %q: %v", c.in, err)
		}
		if got := redactURL(u); got != c.want {
			t.Errorf("redactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
