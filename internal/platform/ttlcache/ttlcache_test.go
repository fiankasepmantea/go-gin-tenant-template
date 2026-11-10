package ttlcache

import (
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestExpiry(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	c := newWithClock[string, int](10*time.Second, 100, clk.now)

	if _, ok := c.Get("a"); ok {
		t.Fatal("empty cache returned a value")
	}
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get = %v, %v", v, ok)
	}
	clk.advance(9 * time.Second)
	if _, ok := c.Get("a"); !ok {
		t.Error("expired too early")
	}
	clk.advance(1 * time.Second) // exactly at the TTL: expired
	if _, ok := c.Get("a"); ok {
		t.Error("an entry must expire after the TTL")
	}
	if c.Len() != 0 {
		t.Error("an expired entry is removed when it is read")
	}
}

func TestSetRefreshesTheTTL(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	c := newWithClock[string, int](10*time.Second, 100, clk.now)
	c.Set("a", 1)
	clk.advance(8 * time.Second)
	c.Set("a", 2)
	clk.advance(8 * time.Second)
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Errorf("Get = %v, %v; want 2, true", v, ok)
	}
}

func TestDeleteAndClear(t *testing.T) {
	c := New[int, string](time.Minute, 10)
	c.Set(1, "a")
	c.Set(2, "b")
	c.Delete(1)
	if _, ok := c.Get(1); ok {
		t.Error("Delete did not remove the key")
	}
	if _, ok := c.Get(2); !ok {
		t.Error("Delete removed another key")
	}
	c.Clear()
	if c.Len() != 0 {
		t.Error("Clear did not empty the cache")
	}
}

func TestMaxEntriesIsBounded(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	c := newWithClock[int, int](10*time.Second, 3, clk.now)
	for i := 0; i < 3; i++ {
		c.Set(i, i)
	}
	clk.advance(11 * time.Second) // all three are expired
	c.Set(10, 10)                 // makes room by dropping the expired ones
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1 after dropping expired entries", c.Len())
	}

	for i := 0; i < 3; i++ { // fill with live entries
		c.Set(100+i, i)
	}
	c.Set(200, 200) // full of live entries: the cache is cleared, never unbounded
	if c.Len() > 3 {
		t.Errorf("Len = %d, must never exceed max", c.Len())
	}
	if _, ok := c.Get(200); !ok {
		t.Error("the new entry must be stored")
	}
}

func TestOverwriteWhenFullDoesNotEvict(t *testing.T) {
	c := New[int, int](time.Minute, 2)
	c.Set(1, 1)
	c.Set(2, 2)
	c.Set(1, 11) // key exists: no eviction
	if v, ok := c.Get(2); !ok || v != 2 {
		t.Error("overwriting an existing key must not clear the cache")
	}
}

func TestNewPanicsOnBadTTL(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a non-positive ttl must panic")
		}
	}()
	New[int, int](0, 1)
}

func TestConcurrentUse(t *testing.T) {
	c := New[int, int](time.Minute, 50)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				c.Set(i%80, i)
				c.Get(i % 80)
				if i%97 == 0 {
					c.Delete(i % 80)
				}
			}
		}()
	}
	wg.Wait()
}
