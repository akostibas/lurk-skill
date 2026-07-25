// Package macos wraps the bits of the host OS both sources need.
//
// Slack and Signal both store their encryption key as a Chromium "Safe Storage"
// Keychain password, so both shell out to `security` and both have to explain
// the same handful of failures to the user. Doing that in one place keeps the
// wording consistent and stops raw exit codes reaching a digest.
package macos

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
)

// ErrNoKeychainItem reports that the Keychain has no such item at all — as
// opposed to access being denied. Callers use it to tell "this app has never
// run on this Mac" apart from "the user dismissed the prompt", which need
// completely different advice.
var ErrNoKeychainItem = errors.New("no such Keychain item")

// `security` exit codes we can act on. Anything else is reported verbatim.
const (
	exitItemNotFound = 44 // errSecItemNotFound
	exitAuthFailed   = 51 // errSecAuthFailed — prompt dismissed or denied
)

// Secret returns the password stored under the given generic-password service
// name (e.g. "Slack Safe Storage").
func Secret(service string) ([]byte, error) {
	out, err := exec.Command("security", "find-generic-password", "-ws", service).Output()
	if err == nil {
		return bytes.TrimRight(out, "\n"), nil
	}

	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		switch ee.ExitCode() {
		case exitItemNotFound:
			return nil, fmt.Errorf("%w: %q", ErrNoKeychainItem, service)
		case exitAuthFailed:
			return nil, fmt.Errorf("Keychain access to %q was denied — rerun and click Always Allow", service)
		}
	}
	return nil, fmt.Errorf("reading %q from the Keychain: %w", service, err)
}
