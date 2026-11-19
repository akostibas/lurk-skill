package signal

// Presses Signal Desktop's own buttons through the macOS accessibility API, the
// way a screen reader would. This is lurk's only acting path: see calls.go.

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	char *buf;
	size_t len, cap;
	const char *press;
	int prefix, pressed, nodes;
	int texts; // collect static text instead of button titles
} axwalk;

static char *ax_str(AXUIElementRef el, CFStringRef attr) {
	CFTypeRef v = NULL;
	if (AXUIElementCopyAttributeValue(el, attr, &v) != kAXErrorSuccess || !v) return NULL;
	char *out = NULL;
	if (CFGetTypeID(v) == CFStringGetTypeID()) {
		CFIndex n = CFStringGetMaximumSizeForEncoding(CFStringGetLength(v), kCFStringEncodingUTF8) + 1;
		out = malloc(n);
		if (!CFStringGetCString(v, out, n, kCFStringEncodingUTF8)) { free(out); out = NULL; }
	}
	CFRelease(v);
	return out;
}

static void ax_walk(AXUIElementRef el, axwalk *w, int depth) {
	if (w->pressed || depth > 100 || w->nodes++ > 50000) return;
	char *role = ax_str(el, kAXRoleAttribute);
	if (!w->texts && role && strcmp(role, "AXButton") == 0) {
		char *t = ax_str(el, kAXTitleAttribute);
		if (t && *t) {
			size_t n = strlen(t);
			for (size_t i = 0; i < n; i++) if (t[i] == '\n') t[i] = ' ';
			if (w->len + n + 2 > w->cap) { w->cap = (w->cap + n + 2) * 2; w->buf = realloc(w->buf, w->cap); }
			memcpy(w->buf + w->len, t, n);
			w->len += n;
			w->buf[w->len++] = '\n';
			w->buf[w->len] = 0;
			int hit = w->press && (w->prefix ? strncmp(t, w->press, strlen(w->press)) == 0 : strcmp(t, w->press) == 0);
			if (hit) w->pressed = AXUIElementPerformAction(el, kAXPressAction) == kAXErrorSuccess ? 1 : -1;
		}
		free(t);
	}
	if (w->texts && role && strcmp(role, "AXStaticText") == 0) {
		char *t = ax_str(el, kAXValueAttribute);
		if (t && *t) {
			size_t n = strlen(t);
			for (size_t i = 0; i < n; i++) if (t[i] == '\n') t[i] = ' ';
			if (w->len + n + 2 > w->cap) { w->cap = (w->cap + n + 2) * 2; w->buf = realloc(w->buf, w->cap); }
			memcpy(w->buf + w->len, t, n);
			w->len += n;
			w->buf[w->len++] = '\n';
			w->buf[w->len] = 0;
		}
		free(t);
	}
	free(role);
	CFTypeRef kids = NULL;
	if (AXUIElementCopyAttributeValue(el, kAXChildrenAttribute, &kids) == kAXErrorSuccess && kids) {
		if (CFGetTypeID(kids) == CFArrayGetTypeID()) {
			CFIndex n = CFArrayGetCount(kids);
			for (CFIndex i = 0; i < n; i++) ax_walk(CFArrayGetValueAtIndex(kids, i), w, depth + 1);
		}
		CFRelease(kids);
	}
}

// Returns newline-separated button titles (caller frees). With press set, the
// walk stops at the first matching button and presses it.
static char *ax_scan(int pid, const char *press, int prefix, int texts, int *pressed) {
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	AXUIElementSetMessagingTimeout(app, 3.0);
	// Electron hides its web content from accessibility until asked; resets on app restart.
	AXUIElementSetAttributeValue(app, CFSTR("AXManualAccessibility"), kCFBooleanTrue);
	axwalk w = {0};
	w.press = press;
	w.prefix = prefix;
	w.texts = texts;
	w.cap = 4096;
	w.buf = malloc(w.cap);
	w.buf[0] = 0;
	ax_walk(app, &w, 0);
	CFRelease(app);
	*pressed = w.pressed;
	return w.buf;
}

static int ax_trusted(void) { return AXIsProcessTrusted(); }
*/
import "C"

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

// signalPID finds the running Signal Desktop's main process.
func signalPID() (int, error) {
	out, err := exec.Command("pgrep", "-x", "Signal").Output()
	if err != nil || len(strings.Fields(string(out))) == 0 {
		return 0, errors.New("Signal Desktop isn't running")
	}
	return strconv.Atoi(strings.Fields(string(out))[0])
}

// signalUI is a handle on the running Signal Desktop's button tree.
type signalUI struct{ pid int }

func openSignalUI() (*signalUI, error) {
	if C.ax_trusted() == 0 {
		return nil, errors.New("lurk needs Accessibility permission to press Signal's buttons — " +
			"enable the app running lurk (your terminal) under System Settings → Privacy & Security → Accessibility")
	}
	pid, err := signalPID()
	if err != nil {
		return nil, err
	}
	ui := &signalUI{pid: pid}
	// The tree fills in asynchronously after the first accessibility request.
	if _, err := ui.waitFor(5*time.Second, func(b buttons) bool { return len(b) > 0 }); err != nil {
		return nil, errors.New("Signal Desktop's window isn't readable — is it open (not just in the Dock)?")
	}
	return ui, nil
}

// buttons is the titles of every titled button on screen, in tree order.
type buttons []string

func (b buttons) has(title string) bool { return b.count(title) > 0 }

func (b buttons) count(title string) int {
	n := 0
	for _, t := range b {
		if t == title {
			n++
		}
	}
	return n
}

func (b buttons) hasPrefix(prefix string) bool {
	for _, t := range b {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

func (ui *signalUI) scan(press string, prefix bool) (buttons, bool, error) {
	var cpress *C.char
	if press != "" {
		cpress = C.CString(press)
		defer C.free(unsafe.Pointer(cpress))
	}
	p := C.int(0)
	if prefix {
		p = 1
	}
	var pressed C.int
	raw := C.ax_scan(C.int(ui.pid), cpress, p, 0, &pressed)
	defer C.free(unsafe.Pointer(raw))
	if pressed < 0 {
		return nil, false, fmt.Errorf("Signal refused the press of %q", press)
	}
	return lines(C.GoString(raw)), pressed > 0, nil
}

// texts is every piece of static text on screen, in tree order.
func (ui *signalUI) texts() []string {
	var pressed C.int
	raw := C.ax_scan(C.int(ui.pid), nil, 0, 1, &pressed)
	defer C.free(unsafe.Pointer(raw))
	return lines(C.GoString(raw))
}

func lines(s string) []string {
	var out []string
	for t := range strings.SplitSeq(s, "\n") {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (ui *signalUI) list() (buttons, error) {
	b, _, err := ui.scan("", false)
	return b, err
}

// press presses the first button titled exactly title; false if there is none.
func (ui *signalUI) press(title string) (bool, error) {
	_, ok, err := ui.scan(title, false)
	return ok, err
}

func (ui *signalUI) pressPrefix(prefix string) (bool, error) {
	_, ok, err := ui.scan(prefix, true)
	return ok, err
}

// waitFor polls the button tree until ok accepts it, returning that snapshot.
func (ui *signalUI) waitFor(timeout time.Duration, ok func(buttons) bool) (buttons, error) {
	deadline := time.Now().Add(timeout)
	for {
		b, err := ui.list()
		if err != nil {
			return nil, err
		}
		if ok(b) {
			return b, nil
		}
		if time.Now().After(deadline) {
			return b, errors.New("timed out waiting for Signal's interface")
		}
		time.Sleep(250 * time.Millisecond)
	}
}
