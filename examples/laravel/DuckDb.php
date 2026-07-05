<?php

namespace App\Services;

use Illuminate\Support\Collection;
use Illuminate\Support\Facades\Http;
use RuntimeException;

/**
 * DuckDB Lakehouse API 客户端。
 *
 * config/services.php:
 *     'duckdb' => [
 *         'url' => env('DUCKDB_API_URL', 'http://localhost:8080'),
 *         'key' => env('DUCKDB_API_KEY', ''),
 *     ],
 *
 * .env:
 *     DUCKDB_API_URL=http://duckdb-api:8080   # 同一 docker network 内
 *     DUCKDB_API_KEY=changeme
 */
class DuckDb
{
    /**
     * 执行只读 SQL,返回关联数组集合(每行一个 ['col' => value] 数组)。
     *
     *     DuckDb::select('SELECT * FROM orders WHERE year = ? LIMIT 100', [2024]);
     */
    public static function select(string $sql, array $args = []): Collection
    {
        $data = self::request('/api/query', [
            'sql'    => $sql,
            'args'   => $args,
            'format' => 'assoc',
        ]);

        return collect($data['rows']);
    }

    /**
     * 执行只读 SQL,返回原始结构:columns / types / rows / row_count /
     * truncated / duration_ms。适合大结果集(行是数组而不是对象,体积更小)。
     */
    public static function query(string $sql, array $args = [], int $maxRows = 0): array
    {
        return self::request('/api/query', array_filter([
            'sql'      => $sql,
            'args'     => $args,
            'max_rows' => $maxRows,
        ]));
    }

    /** 已注册的表列表(凭证已脱敏)。 */
    public static function tables(): array
    {
        return self::request('/api/tables', null, 'get')['tables'];
    }

    /** 表结构(DESCRIBE)。 */
    public static function schema(string $table): array
    {
        return self::request("/api/tables/{$table}/schema", null, 'get');
    }

    /**
     * 注册一个 S3 上的 parquet 表(等效 CREATE SECRET + CREATE VIEW)。
     * 一般用 UI 操作,程序化注册时可用本方法。
     */
    public static function createTable(array $definition): array
    {
        return self::request('/api/tables', $definition);
    }

    private static function request(string $path, ?array $payload, string $method = 'post'): array
    {
        $pending = Http::withHeaders(['X-API-Key' => config('services.duckdb.key')])
            ->timeout((int) config('services.duckdb.timeout', 300))
            ->acceptJson();

        $response = $method === 'get'
            ? $pending->get(config('services.duckdb.url').$path)
            : $pending->post(config('services.duckdb.url').$path, $payload);

        $body = $response->json();

        if (! $response->successful() || ($body['code'] ?? -1) !== 0) {
            throw new RuntimeException('DuckDB API: '.($body['message'] ?? $response->status()));
        }

        return $body['data'];
    }
}
