package cache

import (
	"context"
	"github.com/maoni/backend-takehome/internal/model"
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

type MemoryBusinessCache struct{ Items map[string]model.Business }

func NewMemoryBusinessCache() *MemoryBusinessCache {
	return &MemoryBusinessCache{Items: map[string]model.Business{}}
}
func (m *MemoryBusinessCache) GetBusiness(_ context.Context, id string) (model.Business, bool, error) {
	b, ok := m.Items[id]
	return b, ok, nil
}
func (m *MemoryBusinessCache) SetBusiness(_ context.Context, b model.Business, _ time.Duration) error {
	m.Items[b.ID] = b
	return nil
}
func (m *MemoryBusinessCache) DeleteBusiness(_ context.Context, id string) error {
	delete(m.Items, id)
	return nil
}
