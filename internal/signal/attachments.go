package signal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/akostibas/lurk-skill/internal/digest"
)

// attachmentsCol is a SQL column yielding a message's user-visible attachments
// as a JSON array string; withAttachments decodes it. Quotes, link previews and
// long-message overflow live in the same table but aren't things the sender
// attached. editHistoryIndex -1 is the current version of an edited message.
const attachmentsCol = `(SELECT json_group_array(json_object(
		'ref', a.rowid, 'name', a.fileName, 'type', a.contentType, 'size', a.size,
		'downloaded', a.path IS NOT NULL))
	FROM (SELECT rowid, * FROM message_attachments
		WHERE messageId=m.id AND attachmentType='attachment' AND editHistoryIndex=-1
		ORDER BY orderInMessage) a) AS attachments`

// withAttachments replaces each row's attachments JSON string with a decoded
// list, so --json emits an array rather than a string of JSON.
func withAttachments(rows []map[string]any) []map[string]any {
	for _, r := range rows {
		var list []map[string]any
		_ = json.Unmarshal([]byte(asStr(r["attachments"])), &list)
		r["attachments"] = orEmpty(list)
	}
	return rows
}

// attachmentTags renders a row's attachments as "[image: x.png — fetch with: …]".
func attachmentTags(r map[string]any) string {
	list, _ := r["attachments"].([]map[string]any)
	var tags []string
	for _, a := range list {
		kind := strings.SplitN(asStr(a["type"]), "/", 2)[0]
		if kind != "image" && kind != "video" && kind != "audio" {
			kind = "file"
		}
		label := kind
		if n := asStr(a["name"]); n != "" {
			label += ": " + n
		}
		if a["downloaded"] == true || asInt(a["downloaded"]) == 1 {
			label += fmt.Sprintf(" — fetch with: lurk signal file %s", asStr(a["ref"]))
		} else {
			label += " — not on this Mac"
		}
		tags = append(tags, "["+label+"]")
	}
	return strings.Join(tags, " ")
}

// withTags appends a row's attachment tags to its rendered body.
func withTags(body string, r map[string]any) string {
	tags := attachmentTags(r)
	switch {
	case tags == "":
		return body
	case body == "":
		return tags
	}
	return body + " " + tags
}

// cmdFile decrypts one attachment to a file. Signal Desktop encrypts attachments
// at rest; the key sits beside the path in the DB. Signal's own files are only read.
func cmdFile(db *sqliteDB, jsonOut bool, ref, out string) error {
	rows, err := db.query(fmt.Sprintf(`SELECT a.path, a.localKey, a.size, a.fileName, a.contentType,
		a.conversationId AS conv_id, %s AS conv, c.e164
		FROM message_attachments a JOIN conversations c ON c.id = a.conversationId
		WHERE a.rowid = ?`, dispName("c")), ref)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("no attachment %q — refs come from `signal history` or `signal search`", ref)
	}
	if len(keepInScope(rows, "conv_id", "conv")) == 0 {
		return fmt.Errorf("attachment %q is in a conversation outside the declared scope (see: lurk scope)", ref)
	}
	r := rows[0]
	if asStr(r["path"]) == "" {
		return errors.New("that attachment isn't on this Mac — Signal Desktop never downloaded it, " +
			"and Signal's servers only keep attachments for about 45 days")
	}
	raw, err := os.ReadFile(filepath.Join(signalDir(), "attachments.noindex", asStr(r["path"])))
	if err != nil {
		return err
	}
	body, err := decryptAttachment(raw, asStr(r["localKey"]), asInt(r["size"]))
	if err != nil {
		return err
	}

	name := filepath.Base(asStr(r["fileName"]))
	if name == "" || name == "." || name == "/" {
		name = "lurk-signal-" + ref
		if exts, _ := mime.ExtensionsByType(asStr(r["contentType"])); len(exts) > 0 {
			name += exts[0]
		}
	}
	if out == "" {
		out = filepath.Join(os.TempDir(), name)
	}
	if out == "-" {
		_, err = os.Stdout.Write(body)
		return err
	}
	if err := os.WriteFile(out, body, 0o600); err != nil {
		return err
	}
	abs, _ := filepath.Abs(out)
	if jsonOut {
		digest.PrintJSON(map[string]any{"path": abs, "bytes": len(body), "mimetype": asStr(r["contentType"]), "name": name})
		return nil
	}
	fmt.Printf("saved %s (%d bytes, %s)\n", abs, len(body), asStr(r["contentType"]))
	return nil
}

// decryptAttachment reverses Signal Desktop's at-rest format:
// IV(16) ‖ AES-256-CBC ciphertext ‖ HMAC-SHA256(IV‖ciphertext), keyed by the
// 64-byte localKey (AES key ‖ MAC key). Plaintext is padded; size trims it.
func decryptAttachment(raw []byte, localKey string, size int64) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(localKey)
	if err != nil || len(key) != 64 {
		return nil, errors.New("attachment has no usable local key (an older Signal storage format)")
	}
	if len(raw) < 16+aes.BlockSize+32 || (len(raw)-48)%aes.BlockSize != 0 {
		return nil, errors.New("attachment file is truncated or not in the expected format")
	}
	body, tag := raw[:len(raw)-32], raw[len(raw)-32:]
	m := hmac.New(sha256.New, key[32:])
	m.Write(body)
	if !hmac.Equal(m.Sum(nil), tag) {
		return nil, errors.New("attachment failed its integrity check — the file or key doesn't match")
	}
	block, _ := aes.NewCipher(key[:32])
	pt := make([]byte, len(body)-16)
	cipher.NewCBCDecrypter(block, body[:16]).CryptBlocks(pt, body[16:])
	if n := int(pt[len(pt)-1]); n >= 1 && n <= aes.BlockSize && n <= len(pt) {
		pt = pt[:len(pt)-n] // PKCS#7
	}
	if size > 0 && size <= int64(len(pt)) {
		pt = pt[:size]
	}
	return pt, nil
}
