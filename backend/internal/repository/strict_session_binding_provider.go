package repository

import (
	"database/sql"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// ProvideStrictSessionBindingStore 注入永久会话绑定。Redis 只做加速，数据库才是事实来源。
func ProvideStrictSessionBindingStore(db *sql.DB, rdb *redis.Client) service.StrictSessionBindingStore {
	return service.NewCachedStrictSessionBindingStore(
		NewStrictSessionBindingRepository(db),
		NewStrictSessionBindingCache(rdb),
	)
}
