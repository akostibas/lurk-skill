package signal

// Minimal read-only SQLCipher wrapper over the vendored SQLCipher amalgamation
// (sqlite3.c, built with the CommonCrypto provider). We only ever run SELECTs,
// so this exposes just enough of the C API to key a DB and read typed rows —
// far less surface than a full database/sql driver, and self-contained on macOS
// (no OpenSSL/libtomcrypt; Apple's Security framework supplies the crypto).

/*
#cgo CFLAGS: -DSQLITE_HAS_CODEC -DSQLCIPHER_CRYPTO_CC -DSQLITE_TEMP_STORE=2
#cgo CFLAGS: -DSQLITE_EXTRA_INIT=sqlcipher_extra_init -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown
#cgo CFLAGS: -DSQLITE_ENABLE_FTS5 -DSQLITE_ENABLE_JSON1 -DSQLITE_THREADSAFE=1
#cgo CFLAGS: -DSQLITE_DEFAULT_MEMSTATUS=0 -DNDEBUG -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Security -framework Foundation
#include <stdlib.h>
#include "sqlite3.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type sqliteDB struct {
	h *C.sqlite3
}

// openSQLCipher opens an encrypted DB read-only and keys it with a raw 64-hex
// SQLCipher key, applying SQLCipher-4 defaults. path should be a private copy
// (see openSignalDB) so we never contend with the running Signal app.
func openSQLCipher(path, hexKey string) (*sqliteDB, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	var h *C.sqlite3
	rc := C.sqlite3_open_v2(cpath, &h, C.SQLITE_OPEN_READWRITE, nil)
	if rc != C.SQLITE_OK {
		msg := C.GoString(C.sqlite3_errmsg(h))
		C.sqlite3_close(h)
		return nil, fmt.Errorf("open: %s", msg)
	}
	db := &sqliteDB{h: h}

	// Key first, then declare SQLCipher-4 compatibility. Order matters: the key
	// PRAGMA must be the first statement on the connection.
	if err := db.exec(fmt.Sprintf(`PRAGMA key = "x'%s'"`, hexKey)); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.exec("PRAGMA cipher_compatibility = 4"); err != nil {
		db.Close()
		return nil, err
	}
	// Force decryption/verification now so a bad key fails here, not mid-query.
	if err := db.exec("SELECT count(*) FROM sqlite_master"); err != nil {
		db.Close()
		return nil, fmt.Errorf("decrypt (wrong key or cipher settings?): %w", err)
	}
	return db, nil
}

func (db *sqliteDB) Close() {
	if db.h != nil {
		C.sqlite3_close(db.h)
		db.h = nil
	}
}

func (db *sqliteDB) exec(sql string) error {
	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))
	var errmsg *C.char
	rc := C.sqlite3_exec(db.h, csql, nil, nil, &errmsg)
	if rc != C.SQLITE_OK {
		msg := C.GoString(errmsg)
		C.sqlite3_free(unsafe.Pointer(errmsg))
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// query runs a SELECT and materializes all rows. Positional args bind to ?N and
// may be string or int64/int. Column values come back as string, int64, float64,
// []byte, or nil — enough for everything we read.
func (db *sqliteDB) query(sql string, args ...any) ([]map[string]any, error) {
	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))

	var stmt *C.sqlite3_stmt
	if rc := C.sqlite3_prepare_v2(db.h, csql, -1, &stmt, nil); rc != C.SQLITE_OK {
		return nil, fmt.Errorf("prepare: %s", C.GoString(C.sqlite3_errmsg(db.h)))
	}
	defer C.sqlite3_finalize(stmt)

	for i, a := range args {
		idx := C.int(i + 1)
		switch v := a.(type) {
		case string:
			cs := C.CString(v)
			// SQLITE_TRANSIENT tells SQLite to copy the bytes.
			C.sqlite3_bind_text(stmt, idx, cs, -1, C.SQLITE_TRANSIENT)
			C.free(unsafe.Pointer(cs))
		case int:
			C.sqlite3_bind_int64(stmt, idx, C.sqlite3_int64(v))
		case int64:
			C.sqlite3_bind_int64(stmt, idx, C.sqlite3_int64(v))
		case nil:
			C.sqlite3_bind_null(stmt, idx)
		default:
			return nil, fmt.Errorf("unsupported bind arg %d: %T", i, a)
		}
	}

	ncol := int(C.sqlite3_column_count(stmt))
	cols := make([]string, ncol)
	for i := 0; i < ncol; i++ {
		cols[i] = C.GoString(C.sqlite3_column_name(stmt, C.int(i)))
	}

	var out []map[string]any
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			break
		}
		if rc != C.SQLITE_ROW {
			return nil, fmt.Errorf("step: %s", C.GoString(C.sqlite3_errmsg(db.h)))
		}
		row := make(map[string]any, ncol)
		for i := 0; i < ncol; i++ {
			ci := C.int(i)
			switch C.sqlite3_column_type(stmt, ci) {
			case C.SQLITE_INTEGER:
				row[cols[i]] = int64(C.sqlite3_column_int64(stmt, ci))
			case C.SQLITE_FLOAT:
				row[cols[i]] = float64(C.sqlite3_column_double(stmt, ci))
			case C.SQLITE_TEXT:
				n := C.sqlite3_column_bytes(stmt, ci)
				p := unsafe.Pointer(C.sqlite3_column_text(stmt, ci))
				row[cols[i]] = C.GoStringN((*C.char)(p), n)
			case C.SQLITE_BLOB:
				n := C.sqlite3_column_bytes(stmt, ci)
				p := C.sqlite3_column_blob(stmt, ci)
				row[cols[i]] = C.GoBytes(p, n)
			default: // SQLITE_NULL
				row[cols[i]] = nil
			}
		}
		out = append(out, row)
	}
	return out, nil
}
