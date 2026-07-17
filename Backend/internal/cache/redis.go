package cache

import (
	"context"
	"fmt"

	"github.com/projectrebound/matchserver/internal/config"
	"github.com/redis/go-redis/v9"
)

type Client struct {
	*redis.Client
}

func Open(ctx context.Context, cfg config.RedisConfig) (*Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Address,
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.ConnectTimeout(),
		ReadTimeout:  cfg.OperationTimeout(),
		WriteTimeout: cfg.OperationTimeout(),
	})
	wrapper := &Client{Client: client}
	if err := wrapper.Check(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return wrapper, nil
}

func (c *Client) Check(ctx context.Context) error {
	if err := c.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("Redis ping: %w", err)
	}
	return nil
}
