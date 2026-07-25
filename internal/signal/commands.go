package signal

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/akostibas/lurk-skill/internal/digest"
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

func fmtTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// --- conversation resolution ---

type conv struct {
	ID, Type, Name, E164 string
}

func resolveConv(db *sqliteDB, arg string) (*conv, error) {
	// Exact conversation id.
	if rows, _ := db.query(
		fmt.Sprintf("SELECT id, type, %s AS name, e164 FROM conversations c WHERE id=?", dispName("c")),
		arg); len(rows) == 1 {
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
	switch len(rows) {
	case 0:
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

func cmdSearch(db *sqliteDB, jsonOut bool, query, convArg string, count int) error {
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
	q := fmt.Sprintf(`SELECT m.rowid, m.type, m.sent_at, m.body, %[1]s AS conv, %[2]s AS sender
		FROM messages m
		JOIN conversations c ON c.id = m.conversationId
		LEFT JOIN conversations s ON s.serviceId = m.sourceServiceId
		WHERE %[3]s ORDER BY m.received_at DESC LIMIT %[4]d`,
		dispName("c"), dispName("s"), where, count)
	rows, err := db.query(q, args...)
	if err != nil {
		return err
	}
	if jsonOut {
		printJSON(rows)
		return nil
	}
	fmt.Printf("%d matches for %q%s:\n", len(rows), query, label)
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

// summaryRows splits active conversations into those Signal considers unread
// (its stored unreadCount, plus manually marked-unread) and those merely active
// within the window. Shared by `signal summary` and the cross-source digest.
func summaryRows(db *sqliteDB, hours int) (unread, recent []map[string]any, err error) {
	sinceMs := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
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
	for _, r := range rows {
		if asInt(r["unread"]) > 0 || asInt(r["markedUnread"]) == 1 {
			unread = append(unread, r)
		} else if asInt(r["last_at"]) >= sinceMs {
			recent = append(recent, r)
		}
	}
	return unread, recent, nil
}

// digestItems turns the summary rows into source-neutral digest items.
func digestItems(db *sqliteDB, hours int) ([]digest.Item, error) {
	unread, recent, err := summaryRows(db, hours)
	if err != nil {
		return nil, err
	}
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
		items = append(items, item(digest.Unread, r, truncate(oneLine(asStr(r["last_body"])), 78), note))
	}
	for _, r := range recent {
		items = append(items, item(digest.Recent, r, "", ""))
	}
	return items, nil
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

func cmdSummary(db *sqliteDB, jsonOut bool, hours int) error {
	if jsonOut {
		unread, recent, err := summaryRows(db, hours)
		if err != nil {
			return err
		}
		printJSON(map[string]any{"unread": unread, "recent": recent})
		return nil
	}
	items, err := digestItems(db, hours)
	if err != nil {
		return err
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
