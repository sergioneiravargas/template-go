package cache_test

import (
	"template-go/pkg/cache"
	"testing"
)

func TestCache(t *testing.T) {
	cache := cache.New[string, string]()

	key1 := "key_1"
	value1 := "value_1"

	key2 := "key_2"
	value2 := "value_2"

	cache.Set(key1, value1)
	cache.Set(key2, value2)

	value, ok := cache.Get(key1)
	if !ok {
		t.Errorf("expected value to be found for key '%s'", key1)
	}

	if value != value1 {
		t.Errorf("expected value to be '%s'", value1)
	}

	value, ok = cache.Get(key2)
	if !ok {
		t.Errorf("expected value to be found for key '%s'", key2)
	}

	if value != value2 {
		t.Errorf("expected value to be '%s'", value2)
	}

	cache.Unset(key1)
	_, ok = cache.Get(key1)
	if ok {
		t.Errorf("expected value not to be found for key '%s'", key1)
	}

	cache.Unset(key2)
	_, ok = cache.Get(key2)
	if ok {
		t.Errorf("expected value not to be found for key '%s'", key2)
	}
}
