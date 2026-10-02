package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const strictSessionBindingCachePrefix = "strict_session_binding:"

type strictSessionBindingCache struct {
	rdb *redis.Client
}

// NewStrictSessionBindingCache 创建不带 TTL 的绑定加速缓存。
// 键丢失时调用方必须回源数据库，不能把缓存未命中当成未绑定。
func NewStrictSessionBindingCache(rdb *redis.Client) service.StrictBindingCache {
	return &strictSessionBindingCache{rdb: rdb}
}

func strictSessionBindingCacheKey(bindingKey string) string {
	return strictSessionBindingCachePrefix + bindingKey
}

func (c *strictSessionBindingCache) Get(ctx context.Context, bindingKey string) (int64, bool, error) {
	if c == nil || c.rdb == nil || bindingKey == "" {
		return 0, false, nil
	}
	value, err := c.rdb.Get(ctx, strictSessionBindingCacheKey(bindingKey)).Result()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	accountID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || accountID <= 0 {
		return 0, false, fmt.Errorf("invalid strict session cache value")
	}
	return accountID, true, nil
}

func (c *strictSessionBindingCache) Set(ctx context.Context, bindingKey string, accountID int64) error {
	if c == nil || c.rdb == nil || bindingKey == "" || accountID <= 0 {
		return nil
	}
	// expiration 0：加速项不因空闲时间过期。Redis 被清空后数据库仍是事实来源。
	return c.rdb.Set(ctx, strictSessionBindingCacheKey(bindingKey), strconv.FormatInt(accountID, 10), time.Duration(0)).Err()
}
