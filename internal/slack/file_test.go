package slack

import "testing"

// isSlackFileHost is the blast-radius guard on `slack file`: the download path
// carries the user's bearer token, so it must send that token ONLY to Slack's
// own file hosts. A regression here would leak the session token to an arbitrary
// URL embedded in a message, so pin the accept/reject set.
func TestIsSlackFileHostAllowsOnlySlackHosts(t *testing.T) {
	allow := []string{
		"files.slack.com",
		"slack-files.com",
		"files-edge.slack.com", // subdomain of slack.com
		"FILES.SLACK.COM",      // case-insensitive
	}
	for _, h := range allow {
		if !isSlackFileHost(h) {
			t.Errorf("isSlackFileHost(%q) = false, want true", h)
		}
	}

	reject := []string{
		"",
		"example.com",
		"evil.com",
		"slack.com.evil.com",      // suffix-spoof: not a real slack.com host
		"notslack.com",            // must not match on a bare "slack.com" substring
		"files.slack.com.evil.io", // trailing-domain spoof
	}
	for _, h := range reject {
		if isSlackFileHost(h) {
			t.Errorf("isSlackFileHost(%q) = true, want false", h)
		}
	}
}

// download must refuse any non-Slack or non-https URL before it ever attaches
// the bearer token and makes a request.
func TestDownloadRefusesNonSlackURL(t *testing.T) {
	c := &client{token: "xoxc-test", cookie: "d=xoxd-test"}
	for _, u := range []string{
		"https://evil.com/steal",
		"http://files.slack.com/files-pri/x", // not https
		"ftp://files.slack.com/x",
		"not a url at all ::::",
	} {
		if _, _, err := c.download(u); err == nil {
			t.Errorf("download(%q) returned nil error; expected a refusal", u)
		}
	}
}
