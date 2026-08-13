package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

// idSeq is the fallback source of uniqueness if the system entropy pool is
// unavailable. It is process-local, which is enough because it is only reached
// when crypto/rand fails.
var idSeq atomic.Uint64

// newID returns a time-ordered, collision-resistant identifier.
//
// time.Now().UnixNano() on its own is NOT unique. The clock's resolution is
// coarser than a nanosecond on some platforms, so two goroutines creating a
// review at the same moment can read the same instant and mint the same ID.
// Concurrent-writer tests reproduce that collision reliably; against a real
// database it is a duplicate key error, or worse, one write silently
// overwriting another.
//
// The timestamp prefix is kept so identifiers stay roughly sortable by
// creation time, which is useful in logs and when scanning the collection by
// hand. Production on MongoDB would more likely let the driver mint an
// ObjectID, which carries the same property.
func newID(prefix string) string {
	now := time.Now().UnixNano()
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// Entropy failure must never fail a customer's write.
		return fmt.Sprintf("%s_%d_%d", prefix, now, idSeq.Add(1))
	}
	return fmt.Sprintf("%s_%d_%s", prefix, now, hex.EncodeToString(suffix[:]))
}
