package cache

import (
	"context"

	"github.com/redis/go-redis/v9"
)

func Open(ctx context.Context, rawURL string) (*redis.Client, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}
