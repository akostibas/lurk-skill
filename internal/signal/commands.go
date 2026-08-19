package signal

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/akostibas/lurk-skill/internal/digest"
	"github.com/akostibas/lurk-skill/internal/scope"
)

// dispName is a SQL expression yielding a human display name for a conversation
// row under the given alias: system/contact name, else profile name, else phone
// number, else the raw id. Groups always have `name`.
func dispName(alias string) string {
	a := alias
	return fmt.Sprintf(
		"COALESCE(NULLIF(TRIM(%[1]s.name),''), NULLIF(TRIM(%[1]s.profileFullName),''), "+
			"NULLIF(TRIM(%[1]s.profileName),''), %[1]s.e164, %[1]s.id)", a)
}

// --- value helpers over query()'s map rows ---

func asStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%g", t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func asInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	default:
		return 0
	}
}

// tsLayout is the timestamp format in every text-mode line. It is a de-facto
// output contract: `signal history` and `slack search|history` print it as a
// leading "[YYYY-MM-DD HH:MM]" and downstream callers parse it to window
// results. Changing it fails *silently* — a caller's filter simply matches
// nothing and the run looks like a quiet day rather than an error. Keep it
// in step with slack's fmtTS, and see the tests pinning both.
const tsLayout = "2006-01-02 15:04"

func fmtTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Local().Format(tsLayout)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// --- scope ---

// keepInScope drops rows whose conversation isn't in the declared scope and
// records how many went, so a filtered list stays distinguishable from a quiet
// one. Row shapes differ between queries, hence the key names.
func keepInScope(rows []map[string]any, idKey, nameKey string) []map[string]any {
	s := scope.Current()
	if s == nil {
		return rows
	}
	out := rows[:0:0]
	for _, r := range rows {
		if s.Signal(asStr(r[idKey]), asStr(r[nameKey]), asStr(r["e164"])) {
			out = append(out, r)
		}
	}
	scope.Exclude(len(rows) - len(out))
	return out
}

// outOfScope names the conversations a filter is about to drop, so an error can
// say which one the caller probably meant.
func outOfScope(rows []map[string]any) []string {
	s := scope.Current()
	if s == nil {
		return nil
	}
	var out []string
	for _, r := range rows {
		if !s.Signal(asStr(r["id"]), asStr(r["name"]), asStr(r["e164"])) {
			out = append(out, asStr(r["name"]))
		}
	}
	return out
}

// --- conversation resolution ---

type conv struct {
	ID, Type, Name, E164 string
}

func resolveConv(db *sqliteDB, arg string) (*conv, error) {
	// Exact conversation id. Scope-checked like any other match: naming a
	// conversation by id must not reach further than naming it by name.
	if rows, _ := db.query(
		fmt.Sprintf("SELECT id, type, %s AS name, e164 FROM conversations c WHERE id=?", dispName("c")),
		arg); len(keepInScope(rows, "id", "name")) == 1 {
		return rowConv(rows[0]), nil
	}
	// Substring match on display name or phone, most-recent first.
	q := fmt.Sprintf(`SELECT id, type, %[1]s AS name, e164 FROM conversations c
		WHERE active_at IS NOT NULL AND (%[1]s LIKE '%%'||?||'%%' OR e164 LIKE '%%'||?||'%%')
		ORDER BY active_at DESC`, dispName("c"))
	rows, err := db.query(q, arg, arg)
	if err != nil {
		return nil, err
	}
	// Held before filtering: naming the conversation the caller meant is what
	// lets the error say which line would include it.
	dropped := outOfScope(rows)
	rows = keepInScope(rows, "id", "name")
	switch len(rows) {
	case 0:
		// Distinguish "no such conversation" from "it's there but excluded":
		// the second sends a caller off diagnosing the wrong thing.
		if len(dropped) == 1 {
			return nil, fmt.Errorf("conversation %q is outside the declared scope — "+
				"add a line `signal %[1]s` to %s to include it (see: lurk scope)", dropped[0], scope.Path())
		}
		if len(dropped) > 1 {
			return nil, fmt.Errorf("%q matches %d conversations, all outside the declared scope (%s); "+
				"add one to %s to include it (see: lurk scope)",
				arg, len(dropped), strings.Join(dropped, ", "), scope.Path())
		}
		return nil, fmt.Errorf("no conversation matches %q", arg)
	case 1:
		return rowConv(rows[0]), nil
	}
	// Prefer a unique exact (case-insensitive) name hit before giving up.
	var exact []map[string]any
	for _, r := range rows {
		if strings.EqualFold(asStr(r["name"]), arg) {
			exact = append(exact, r)
		}
	}
	if len(exact) == 1 {
		return rowConv(exact[0]), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q is ambiguous — matches %d conversations:\n", arg, len(rows))
	for i, r := range rows {
		if i >= 8 {
			fmt.Fprintf(&b, "  … and %d more\n", len(rows)-8)
			break
		}
		fmt.Fprintf(&b, "  %s  (%s)  id=%s\n", asStr(r["name"]), asStr(r["type"]), asStr(r["id"]))
	}
	return nil, fmt.Errorf("%s", b.String())
}

func rowConv(r map[string]any) *conv {
	return &conv{ID: asStr(r["id"]), Type: asStr(r["type"]), Name: asStr(r["name"]), E164: asStr(r["e164"])}
}

// --- commands ---

func cmdConversations(db *sqliteDB, jsonOut bool, filter, kind string, limit int) error {
	where := "c.active_at IS NOT NULL"
	var args []any
	switch kind {
	case "dms":
		where += " AND c.type='private'"
	case "groups":
		where += " AND c.type='group'"
	}
	if filter != "" {
		where += fmt.Sprintf(" AND %s LIKE '%%'||?||'%%'", dispName("c"))
		args = append(args, filter)
	}
	q := fmt.Sprintf(`SELECT c.id, c.type, %[1]s AS name, c.active_at,
		(SELECT m.sent_at FROM messages m WHERE m.conversationId=c.id ORDER BY m.received_at DESC LIMIT 1) AS last_at,
		(SELECT m.body FROM messages m WHERE m.conversationId=c.id AND m.body IS NOT NULL AND m.body<>''
		   ORDER BY m.received_at DESC LIMIT 1) AS last_body,
		COALESCE(json_extract(c.json,'$.unreadCount'),0) AS unread
		FROM conversations c WHERE %[2]s ORDER BY c.active_at DESC LIMIT %[3]d`,
		dispName("c"), where, limit)
	rows, err := db.query(q, args...)
	if err != nil {
		return err
	}
	rows = keepInScope(rows, "id", "name")
	if jsonOut {
		printJSON(rows)
		return nil
	}
	for _, r := range rows {
		tag := "  "
		if asStr(r["type"]) == "group" {
			tag = "# "
		}
		unread := asInt(r["unread"])
		badge := ""
		if unread > 0 {
			badge = fmt.Sprintf("  (%d unread)", unread)
		}
		fmt.Printf("%s%-28s  %s%s\n", tag, truncate(asStr(r["name"]), 28), fmtTime(asInt(r["last_at"])), badge)
		if body := asStr(r["last_body"]); body != "" {
			fmt.Printf("     %s\n", truncate(oneLine(body), 78))
		}
	}
	return nil
}

func cmdHistory(db *sqliteDB, jsonOut bool, convArg string, limit int, before int64) error {
	c, err := resolveConv(db, convArg)
	if err != nil {
		return err
	}
	where := "m.conversationId=? AND m.type IN ('incoming','outgoing','call-history')"
	args := []any{c.ID}
	if before > 0 {
		where += " AND m.rowid < ?"
		args = append(args, before)
	}
	q := fmt.Sprintf(`SELECT m.rowid, m.type, m.sent_at, m.body, m.hasAttachments,
		%s AS sender
		FROM messages m LEFT JOIN conversations s ON s.serviceId = m.sourceServiceId
		WHERE %s ORDER BY m.received_at DESC LIMIT %d`, dispName("s"), where, limit)
	rows, err := db.query(q, args...)
	if err != nil {
		return err
	}
	// Fetched newest-first; print oldest-first.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if jsonOut {
		printJSON(rows)
		return nil
	}
	fmt.Printf("=== %s (%s) ===\n", c.Name, c.Type)
	var minRow int64
	for _, r := range rows {
		rid := asInt(r["rowid"])
		if minRow == 0 || rid < minRow {
			minRow = rid
		}
		sender := asStr(r["sender"])
		if asStr(r["type"]) == "outgoing" {
			sender = "me"
		} else if sender == "" {
			sender = "?"
		}
		body := oneLine(asStr(r["body"]))
		switch {
		case asStr(r["type"]) == "call-history":
			body = "[call]"
		case body == "" && asInt(r["hasAttachments"]) > 0:
			body = "[attachment]"
		}
		fmt.Printf("[%s] %s: %s\n", fmtTime(asInt(r["sent_at"])), sender, body)
	}
	if len(rows) == limit && minRow > 0 {
		fmt.Printf("\n(more above — page with: history %q --before %d)\n", convArg, minRow)
	}
	return nil
}

func cmdSearch(db *sqliteDB, jsonOut bool, query, convArg string, count, before, after int) error {
	where := "m.body LIKE '%'||?||'%' AND m.type IN ('incoming','outgoing')"
	args := []any{query}
	label := ""
	if convArg != "" {
		c, err := resolveConv(db, convArg)
		if err != nil {
			return err
		}
		where += " AND m.conversationId=?"
		args = append(args, c.ID)
		label = " in " + c.Name
	}
	q := fmt.Sprintf(`SELECT m.rowid, m.conversationId AS conv_id, m.type, m.sent_at,
		m.body, m.hasAttachments, %[1]s AS conv, %[2]s AS sender
		FROM messages m
		JOIN conversations c ON c.id = m.conversationId
		LEFT JOIN conversations s ON s.serviceId = m.sourceServiceId
		WHERE %[3]s ORDER BY m.received_at DESC LIMIT %[4]d`,
		dispName("c"), dispName("s"), where, count)
	rows, err := db.query(q, args...)
	if err != nil {
		return err
	}
	rows = keepInScope(rows, "conv_id", "conv")

	// With context requested, attach the surrounding messages to each hit.
	// Both keys are always present (empty when that side is 0) so the JSON shape
	// stays predictable for a consumer rather than flipping to null.
	if before > 0 || after > 0 {
		for _, m := range rows {
			pre, post, err := contextRows(db, asStr(m["conv_id"]), asInt(m["rowid"]), before, after)
			if err != nil {
				return err
			}
			m["context_before"] = orEmpty(pre)
			m["context_after"] = orEmpty(post)
		}
	}

	if jsonOut {
		printJSON(rows)
		return nil
	}

	fmt.Printf("%d matches for %q%s:\n", len(rows), query, label)

	// No context: the flat, one-line-per-hit form, each line naming its own
	// conversation since hits can span many.
	if before == 0 && after == 0 {
		for _, r := range rows {
			sender := asStr(r["sender"])
			if asStr(r["type"]) == "outgoing" {
				sender = "me"
			}
			fmt.Printf("[%s] %s → %s: %s\n", fmtTime(asInt(r["sent_at"])),
				sender, asStr(r["conv"]), truncate(oneLine(asStr(r["body"])), 90))
		}
		return nil
	}

	// With context: group each hit under its conversation, indent the
	// surrounding messages, and mark the matched line with ">".
	for _, m := range rows {
		fmt.Printf("\n# %s\n", asStr(m["conv"]))
		for _, r := range m["context_before"].([]map[string]any) {
			fmt.Printf("    %s\n", sigLine(r))
		}
		fmt.Printf("  > %s\n", sigLine(m))
		for _, r := range m["context_after"].([]map[string]any) {
			fmt.Printf("    %s\n", sigLine(r))
		}
	}
	return nil
}

// sigLine renders one conversation message as "[time] sender: body", the same
// shape `signal history` prints. Used for both the matched line and its
// surrounding context; the conversation name lives in the group header instead.
func sigLine(r map[string]any) string {
	sender := asStr(r["sender"])
	switch {
	case asStr(r["type"]) == "outgoing":
		sender = "me"
	case sender == "":
		sender = "?"
	}
	body := oneLine(asStr(r["body"]))
	switch {
	case asStr(r["type"]) == "call-history":
		body = "[call]"
	case body == "" && asInt(r["hasAttachments"]) > 0:
		body = "[attachment]"
	}
	return fmt.Sprintf("[%s] %s: %s", fmtTime(asInt(r["sent_at"])), sender, body)
}

// contextRows fetches up to `before` messages immediately preceding and up to
// `after` immediately following the given rowid within one conversation, each
// slice in chronological (rowid) order. rowid is Signal's insertion order, so
// adjacent rowids are adjacent messages — the same cursor `history --before`
// pages on. A hit near the start or end of a conversation simply gets a shorter
// window. Overlapping windows from two nearby hits are not merged; each hit
// prints its own, so a message can appear under more than one.
func contextRows(db *sqliteDB, convID string, rowid int64, before, after int) (pre, post []map[string]any, err error) {
	const sel = `SELECT m.rowid, m.type, m.sent_at, m.body, m.hasAttachments, %s AS sender
		FROM messages m LEFT JOIN conversations s ON s.serviceId = m.sourceServiceId
		WHERE m.conversationId=? AND m.type IN ('incoming','outgoing','call-history')
		  AND m.rowid %s ? ORDER BY m.rowid %s LIMIT %d`
	if before > 0 {
		pre, err = db.query(fmt.Sprintf(sel, dispName("s"), "<", "DESC", before), convID, rowid)
		if err != nil {
			return nil, nil, err
		}
		// Fetched nearest-first (descending); flip to chronological.
		for i, j := 0, len(pre)-1; i < j; i, j = i+1, j-1 {
			pre[i], pre[j] = pre[j], pre[i]
		}
	}
	if after > 0 {
		post, err = db.query(fmt.Sprintf(sel, dispName("s"), ">", "ASC", after), convID, rowid)
		if err != nil {
			return nil, nil, err
		}
	}
	return pre, post, nil
}

// orEmpty turns a nil row slice into an empty one so it marshals as [] not null.
func orEmpty(rows []map[string]any) []map[string]any {
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}

// windowStart is the cutoff `--hours` describes: the start of the activity
// window. It bounds the "recent" bucket only — see summaryRows.
func windowStart(hours int) int64 {
	return time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
}

// summaryRows splits active conversations into those Signal considers unread
// (its stored unreadCount, plus manually marked-unread) and those merely active
// within the window. Shared by `signal summary` and the cross-source digest.
//
// Only the "recent" bucket is windowed, and that asymmetry is deliberate: unread
// is a *state*, recent activity is an *event*. A DM you left unread on Tuesday is
// still waiting on you, so dropping it from `--hours 1` would break the question
// the digest exists to answer. digestItems marks the ones that predate the window
// so their presence is explained rather than surprising.
func summaryRows(db *sqliteDB, hours int) (unread, recent []map[string]any, err error) {
	sinceMs := windowStart(hours)
	q := fmt.Sprintf(`SELECT c.id, c.type, %[1]s AS name,
		COALESCE(json_extract(c.json,'$.unreadCount'),0) AS unread,
		COALESCE(json_extract(c.json,'$.markedUnread'),0) AS markedUnread,
		(SELECT m.sent_at FROM messages m WHERE m.conversationId=c.id ORDER BY m.received_at DESC LIMIT 1) AS last_at,
		(SELECT m.body FROM messages m WHERE m.conversationId=c.id AND m.body IS NOT NULL AND m.body<>''
		   ORDER BY m.received_at DESC LIMIT 1) AS last_body
		FROM conversations c WHERE c.active_at IS NOT NULL
		ORDER BY c.active_at DESC`, dispName("c"))
	rows, err := db.query(q)
	if err != nil {
		return nil, nil, err
	}
	// Bucket first, then scope-filter, so the excluded count reports what would
	// have been in the digest rather than every conversation in the store.
	for _, r := range rows {
		if asInt(r["unread"]) > 0 || asInt(r["markedUnread"]) == 1 {
			unread = append(unread, r)
		} else if asInt(r["last_at"]) >= sinceMs {
			recent = append(recent, r)
		}
	}
	unread, recent = keepInScope(unread, "id", "name"), keepInScope(recent, "id", "name")
	return unread, recent, nil
}

// digestItems turns the summary rows into source-neutral digest items.
func digestItems(db *sqliteDB, hours int) ([]digest.Item, error) {
	unread, recent, err := summaryRows(db, hours)
	if err != nil {
		return nil, err
	}
	return itemsFrom(unread, recent, windowStart(hours)), nil
}

// itemsFrom is the single place summary output is shaped, for both the text and
// JSON forms of `signal summary` and for the cross-source digest. Keeping it
// pure (no DB) is what lets the field set be tested — the JSON form used to be
// built separately from raw rows and quietly carried message bodies the text
// form withheld.
func itemsFrom(unread, recent []map[string]any, sinceMs int64) []digest.Item {
	item := func(k digest.Kind, r map[string]any, text, note string) digest.Item {
		return digest.Item{
			Source: "signal",
			Kind:   k,
			When:   time.UnixMilli(asInt(r["last_at"])),
			Who:    asStr(r["name"]),
			Text:   text,
			Note:   note,
		}
	}
	var items []digest.Item
	for _, r := range unread {
		n := asInt(r["unread"])
		note := "marked unread"
		if n > 0 {
			note = fmt.Sprintf("%d unread", n)
		}
		// An unread conversation is listed however old it is. Say so on the line,
		// otherwise a months-stale DM sitting above this morning's activity looks
		// like the window is broken.
		if last := asInt(r["last_at"]); last > 0 && last < sinceMs {
			note += ", outside the window"
		}
		items = append(items, item(digest.Unread, r, truncate(oneLine(asStr(r["last_body"])), 78), note))
	}
	for _, r := range recent {
		items = append(items, item(digest.Recent, r, "", ""))
	}
	return items
}

// Digest returns catch-up items for Signal, for the cross-source `lurk summary`.
func Digest(hours int) ([]digest.Item, error) {
	db, cleanup, err := openSignalDB()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return digestItems(db, hours)
}

// cmdSummary renders the same items either way. --json used to dump the raw
// rows, which exposed a message body for every recently-active conversation that
// the text form deliberately withheld — a wider surface on the machine-readable
// path, which is the one most likely to end up in a log or another agent's
// context. Both forms now come from itemsFrom, so they can't drift apart again.
func cmdSummary(db *sqliteDB, jsonOut bool, hours int) error {
	items, err := digestItems(db, hours)
	if err != nil {
		return err
	}
	if jsonOut {
		printJSON(items)
		return nil
	}
	digest.Render(os.Stdout, items)
	return nil
}

func cmdWhoami(db *sqliteDB, jsonOut bool) error {
	rows, err := db.query("SELECT id, json_extract(json,'$.value') AS value FROM items WHERE id IN ('number_id','uuid_id')")
	if err != nil {
		return err
	}
	out := map[string]string{}
	for _, r := range rows {
		v := asStr(r["value"])
		if i := strings.IndexByte(v, '.'); i > 0 { // strip trailing ".<deviceId>"
			v = v[:i]
		}
		switch asStr(r["id"]) {
		case "number_id":
			out["phone"] = v
		case "uuid_id":
			out["aci"] = v
		}
	}
	if jsonOut {
		printJSON(out)
		return nil
	}
	fmt.Printf("phone: %s\naci:   %s\n", out["phone"], out["aci"])
	return nil
}

func cmdRaw(db *sqliteDB, query string) error {
	// An arbitrary SELECT can't be bound to a conversation list without parsing
	// SQL, so under a scope it's refused rather than left as a silent hole.
	if err := scope.Refuse("signal raw"); err != nil {
		return err
	}
	t := strings.ToUpper(strings.TrimSpace(query))
	if !(strings.HasPrefix(t, "SELECT") || strings.HasPrefix(t, "WITH") ||
		strings.HasPrefix(t, "PRAGMA") || strings.HasPrefix(t, "EXPLAIN")) {
		return fmt.Errorf("raw only permits read-only SELECT/WITH/PRAGMA/EXPLAIN queries")
	}
	rows, err := db.query(query)
	if err != nil {
		return err
	}
	printJSON(rows)
	return nil
}

// ScopeList prints the conversations the scope admits, resolved against the
// local store — the check that catches a name in the config matching nothing.
func ScopeList(out io.Writer) error {
	db, cleanup, err := openSignalDB()
	if err != nil {
		return err
	}
	defer cleanup()
	q := fmt.Sprintf(`SELECT c.id, c.type, %[1]s AS name, c.e164 FROM conversations c
		WHERE c.active_at IS NOT NULL ORDER BY %[1]s`, dispName("c"))
	rows, err := db.query(q)
	if err != nil {
		return err
	}
	s := scope.Current()
	if s == nil {
		fmt.Fprintf(out, "signal — every conversation (%d)\n", len(rows))
		return nil
	}
	for _, r := range rows {
		if s.Signal(asStr(r["id"]), asStr(r["name"]), asStr(r["e164"])) {
			fmt.Fprintf(out, "signal %s (%s)\n", asStr(r["name"]), asStr(r["type"]))
		}
	}
	return nil
}

// --- small string helpers ---

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
