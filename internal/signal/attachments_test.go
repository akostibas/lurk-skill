package signal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// encryptLikeSignal writes plaintext in Signal Desktop's at-rest format, with
// zero padding before PKCS#7 the way Signal buckets attachment sizes.
func encryptLikeSignal(t *testing.T, pt, key []byte) []byte {
	t.Helper()
	padded := append(append([]byte{}, pt...), make([]byte, 37)...)
	n := aes.BlockSize - len(padded)%aes.BlockSize
	padded = append(padded, bytes.Repeat([]byte{byte(n)}, n)...)
	iv := bytes.Repeat([]byte{7}, 16)
	block, _ := aes.NewCipher(key[:32])
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	body := append(iv, ct...)
	m := hmac.New(sha256.New, key[32:])
	m.Write(body)
	return m.Sum(body)
}

func TestSignalAttachments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(signalDir(), "attachments.noindex", "ab")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{3}, 64)
	pt := []byte("not really a png")
	if err := os.WriteFile(filepath.Join(dir, "blob"), encryptLikeSignal(t, pt, key), 0o600); err != nil {
		t.Fatal(err)
	}

	db := newTestDB(t)
	addConv(t, db, "c1", "Alice")
	addMsg(t, db, "m1", "c1", "Approve?", time.Now(), "incoming")
	ins := `INSERT INTO message_attachments (messageId, conversationId, attachmentType, editHistoryIndex,
		orderInMessage, size, contentType, fileName, path, localKey) VALUES (%s)`
	for _, v := range []string{
		fmt.Sprintf(`'m1','c1','attachment',-1,0,%d,'image/png','shot.png','ab/blob','%s'`,
			len(pt), base64.StdEncoding.EncodeToString(key)),
		`'m1','c1','attachment',-1,1,10,'application/pdf','old.pdf',NULL,NULL`,
		`'m1','c1','preview',-1,0,10,'image/jpeg','link.jpg','ab/x','k'`,
	} {
		if err := db.exec(fmt.Sprintf(ins, v)); err != nil {
			t.Fatal(err)
		}
	}

	out := captureStdout(t, func() {
		if err := cmdHistory(db, false, "Alice", 40, 0); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"Approve? [image: shot.png — fetch with: lurk signal file 1]",
		"[file: old.pdf — not on this Mac]"} {
		if !strings.Contains(out, want) {
			t.Errorf("history should show %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "link.jpg") {
		t.Errorf("link previews aren't attachments:\n%s", out)
	}

	dst := filepath.Join(home, "out.png")
	captureStdout(t, func() {
		if err := cmdFile(db, false, "1", dst); err != nil {
			t.Fatal(err)
		}
	})
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, pt) {
		t.Errorf("decrypted %q, want %q", got, pt)
	}
	if err := cmdFile(db, false, "2", dst); err == nil || !strings.Contains(err.Error(), "45 days") {
		t.Errorf("undownloaded attachment should explain it's gone, got %v", err)
	}
}
