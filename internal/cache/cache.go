package cache

import (
	"context"
	"github.com/maoni/backend-takehome/internal/model"
	"sync"
	"time"
)

type BusinessCache interface {
	GetBusiness(ctx context.Context, businessID string) (model.Business, bool, error)
	SetBusiness(ctx context.Context, business model.Business, ttl time.Duration) error
	DeleteBusiness(ctx context.Context, businessID string) error
}

type NoopBusinessCache struct{}

func (NoopBusinessCache) GetBusiness(context.Context, string) (model.Business, bool, error) {
	return model.Business{}, false, nil
}
func (NoopBusinessCache) SetBusiness(context.Context, model.Business, time.Duration) error {
	return nil
}
func (NoopBusinessCache) DeleteBusiness(context.Context, string) error { return nil }

type MemoryBusinessCache struct {
	mu    sync.RWMutex
	Items map[string]model.Business
}

func NewMemoryBusinessCache() *MemoryBusinessCache {
	return &MemoryBusinessCache{Items: map[string]model.Business{}}
}
func (m *MemoryBusinessCache) GetBusiness(_ context.Context, id string) (model.Business, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.Items[id]
	return b, ok, nil
}
func (m *MemoryBusinessCache) SetBusiness(_ context.Context, b model.Business, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Items[b.ID] = b
	return nil
}
func (m *MemoryBusinessCache) DeleteBusiness(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Items, id)
	return nil
}
