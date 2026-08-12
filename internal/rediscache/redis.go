package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/maoni/backend-takehome/internal/model"
)

type BusinessCache struct{ Client *redis.Client }

func New(addr, password string, db int) *BusinessCache {
	return &BusinessCache{Client: redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})}
}

func (r *BusinessCache) Ping(ctx context.Context) error { return r.Client.Ping(ctx).Err() }
func (r *BusinessCache) Close() error                   { return r.Client.Close() }

func key(id string) string { return "maoni:business:" + id }

func (r *BusinessCache) GetBusiness(ctx context.Context, id string) (model.Business, bool, error) {
	raw, err := r.Client.Get(ctx, key(id)).Bytes()
	if err == redis.Nil {
		return model.Business{}, false, nil
	}
	if err != nil {
		return model.Business{}, false, err
	}
	var b model.Business
	if err := json.Unmarshal(raw, &b); err != nil {
		return model.Business{}, false, fmt.Errorf("decode cached business: %w", err)
	}
	return b, true, nil
}

func (r *BusinessCache) SetBusiness(ctx context.Context, b model.Business, ttl time.Duration) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return r.Client.Set(ctx, key(b.ID), raw, ttl).Err()
}
func (r *BusinessCache) DeleteBusiness(ctx context.Context, id string) error {
	return r.Client.Del(ctx, key(id)).Err()
}
