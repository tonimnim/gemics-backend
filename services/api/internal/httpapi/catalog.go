package httpapi

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *Server) queryGames(ctx context.Context, pool *pgxpool.Pool) ([]map[string]any, error) {
	rows, err := pool.Query(ctx, `
        SELECT id, name, publisher, supported_platforms, result_mode
        FROM games WHERE active = true ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	data := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, publisher, resultMode string
		var platforms []string
		if err := rows.Scan(&id, &name, &publisher, &platforms, &resultMode); err != nil {
			return nil, err
		}
		data = append(data, map[string]any{
			"id": id, "name": name, "publisher": publisher, "platforms": platforms,
			"resultMode": resultMode, "officialIntegration": false,
		})
	}
	return data, rows.Err()
}
