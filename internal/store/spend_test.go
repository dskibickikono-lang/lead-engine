package store

import (
	"testing"
	"time"
)

func TestSpendLedger(t *testing.T) {
	st := openTest(t)
	if got, _ := st.SpendToday("bizraport"); got != 0 {
		t.Errorf("initial spend = %v", got)
	}
	st.AddSpend("bizraport", 1.5)
	st.AddSpend("bizraport", 0.5)
	if got, _ := st.SpendToday("bizraport"); got != 2.0 {
		t.Errorf("spend = %v, want 2.0", got)
	}
	if got, _ := st.SpendToday("other"); got != 0 {
		t.Errorf("other-api spend = %v", got)
	}
}

func TestAPICache(t *testing.T) {
	st := openTest(t)
	if _, ok, _ := st.CacheGet("krs", "0000123456", time.Hour); ok {
		t.Error("empty cache returned a hit")
	}
	st.CachePut("krs", "0000123456", []byte(`{"a":1}`))
	got, ok, err := st.CacheGet("krs", "0000123456", time.Hour)
	if err != nil || !ok || string(got) != `{"a":1}` {
		t.Errorf("cache get: %q ok=%v err=%v", got, ok, err)
	}
	// TTL expiry
	if _, ok, _ := st.CacheGet("krs", "0000123456", -time.Second); ok {
		t.Error("expired entry returned as hit")
	}
}
