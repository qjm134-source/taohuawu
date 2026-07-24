package cost

import (
	"context"
	"testing"
	"time"
)

type fakeEmbeddingAPI struct{}

func (f *fakeEmbeddingAPI) GetEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func (f *fakeEmbeddingAPI) Similarity(a, b []float32) float64 {
	return 0.9
}

func TestLayeredCache_SetAndGet(t *testing.T) {
	cache := NewLayeredCache(CacheConfig{
		Enabled:             true,
		TTL:                 time.Hour,
		MaxEntries:          100,
		SimilarityThreshold: 0.85,
	}, nil, nil)
	defer cache.Stop()

	if _, hit := cache.Get(context.Background(), "hello", "model"); hit {
		t.Error("Get() hit = true before any Set")
	}

	cache.Set(context.Background(), "hello", "world", "model", 100)
	got, hit := cache.Get(context.Background(), "hello", "model")
	if !hit {
		t.Fatal("Get() hit = false after Set")
	}
	if got != "world" {
		t.Errorf("Get() = %q, want %q", got, "world")
	}
}

func TestLayeredCache_GetWithSemantic(t *testing.T) {
	cache := NewLayeredCache(CacheConfig{
		Enabled:             true,
		TTL:                 time.Hour,
		MaxEntries:          100,
		SimilarityThreshold: 0.85,
	}, &fakeEmbeddingAPI{}, nil)
	defer cache.Stop()

	cache.Set(context.Background(), "hello", "world", "model", 100)

	time.Sleep(10 * time.Millisecond)

	got, hit, cacheType := cache.GetWithSemantic(context.Background(), "hello", "model")
	if !hit {
		t.Fatal("GetWithSemantic() hit = false after Set")
	}
	if got != "world" {
		t.Errorf("GetWithSemantic() = %q, want %q", got, "world")
	}
	if cacheType != CacheTypeSemantic {
		t.Errorf("cacheType = %q, want %q", cacheType, CacheTypeSemantic)
	}
}

func TestLayeredCache_Expire(t *testing.T) {
	cache := NewLayeredCache(CacheConfig{
		Enabled:             true,
		TTL:                 10 * time.Millisecond,
		MaxEntries:          100,
		SimilarityThreshold: 0.85,
	}, nil, nil)
	defer cache.Stop()

	cache.Set(context.Background(), "hello", "world", "model", 100)

	time.Sleep(20 * time.Millisecond)

	_, hit := cache.Get(context.Background(), "hello", "model")
	if hit {
		t.Error("Get() hit = true after TTL expired, want false")
	}
}

func TestLayeredCache_EvictOldest(t *testing.T) {
	cache := NewLayeredCache(CacheConfig{
		Enabled:             true,
		TTL:                 time.Hour,
		MaxEntries:          2,
		SimilarityThreshold: 0.85,
	}, nil, nil)
	defer cache.Stop()

	cache.Set(context.Background(), "key1", "value1", "model", 100)
	cache.Set(context.Background(), "key2", "value2", "model", 100)
	cache.Set(context.Background(), "key3", "value3", "model", 100)

	_, hit := cache.Get(context.Background(), "key1", "model")
	if hit {
		t.Error("Get(key1) hit = true after eviction, want false")
	}

	_, hit = cache.Get(context.Background(), "key2", "model")
	if !hit {
		t.Error("Get(key2) hit = false, want true")
	}
}

func TestLayeredCache_SetToolResult(t *testing.T) {
	cache := NewLayeredCache(CacheConfig{
		Enabled: true,
		TTL:     time.Hour,
	}, nil, nil)
	defer cache.Stop()

	result := map[string]string{"weather": "sunny"}
	cache.SetToolResult(context.Background(), "get_weather", "city=beijing", result)

	got, hit := cache.GetToolResult(context.Background(), "get_weather", "city=beijing")
	if !hit {
		t.Fatal("GetToolResult() hit = false after SetToolResult")
	}
	if got.(map[string]string)["weather"] != "sunny" {
		t.Errorf("GetToolResult() = %+v, want weather=sunny", got)
	}
}

func TestLayeredCache_Clear(t *testing.T) {
	cache := NewLayeredCache(CacheConfig{
		Enabled: true,
		TTL:     time.Hour,
	}, nil, nil)
	defer cache.Stop()

	cache.Set(context.Background(), "hello", "world", "model", 100)
	cache.SetToolResult(context.Background(), "tool", "params", "result")

	cache.Clear(context.Background())

	_, hit := cache.Get(context.Background(), "hello", "model")
	if hit {
		t.Error("Get() hit = true after Clear, want false")
	}

	_, hit = cache.GetToolResult(context.Background(), "tool", "params")
	if hit {
		t.Error("GetToolResult() hit = true after Clear, want false")
	}
}

func TestLayeredCache_Delete(t *testing.T) {
	cache := NewLayeredCache(CacheConfig{
		Enabled: true,
		TTL:     time.Hour,
	}, nil, nil)
	defer cache.Stop()

	cache.Set(context.Background(), "hello", "world", "model", 100)
	cache.Set(context.Background(), "hi", "there", "model", 50)

	cache.Delete(context.Background(), "hello")

	_, hit := cache.Get(context.Background(), "hello", "model")
	if hit {
		t.Error("Get(hello) hit = true after Delete, want false")
	}

	_, hit = cache.Get(context.Background(), "hi", "model")
	if !hit {
		t.Error("Get(hi) hit = false, want true")
	}
}

func TestLayeredCache_GetStats(t *testing.T) {
	cache := NewLayeredCache(CacheConfig{
		Enabled: true,
		TTL:     time.Hour,
	}, nil, nil)
	defer cache.Stop()

	stats := cache.GetStats()
	if stats.Hits != 0 || stats.Misses != 0 {
		t.Errorf("initial stats = %+v, want Hits=0, Misses=0", stats)
	}

	cache.Set(context.Background(), "key1", "value1", "model", 100)
	cache.Get(context.Background(), "key1", "model")
	cache.Get(context.Background(), "key2", "model")

	stats = cache.GetStats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Errorf("stats = %+v, want Hits=1, Misses=1", stats)
	}
	if stats.Entries != 1 {
		t.Errorf("stats.Entries = %d, want 1", stats.Entries)
	}
}

func TestLayeredCache_Stop(t *testing.T) {
	cache := NewLayeredCache(CacheConfig{
		Enabled: true,
		TTL:     time.Hour,
	}, nil, nil)

	cache.Stop()

	cache.Set(context.Background(), "key", "value", "model", 100)
	got, hit := cache.Get(context.Background(), "key", "model")
	if !hit {
		t.Fatal("Get() hit = false after Stop, want true")
	}
	if got != "value" {
		t.Errorf("Get() = %q, want %q", got, "value")
	}
}
