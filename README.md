# 🦆 DuckDB Lakehouse API

基于 [GoFrame v2](https://github.com/gogf/gf) + [go-duckdb](https://github.com/marcboeker/go-duckdb) 的 DuckDB 查询服务:

- linux build
```shell
sudo yum install -y gcc11 gcc11-c++ || sudo yum install -y gcc10 gcc10-c++
# export CC=gcc10-cc
# export CXX=gcc10-c++
export CC=gcc11
export CXX=g++11
export CGO_ENABLED=1
go clean -cache
go build -o duckdb-api
```

- linux docker build
```shell
docker run --rm -v "$PWD":/src -w /src golang:1.24-bullseye \
  bash -lc 'CGO_ENABLED=1 go build -o duckdb-api'
```

- **Web UI**:填表单即可把 S3 上的 parquet/csv/json/avro 注册成 DuckDB 表(等效 `INSTALL httpfs` + `CREATE SECRET` + `CREATE VIEW ... read_parquet('s3://...')`),内置 SQL 控制台
- **HTTP API**:给 Laravel(或任何后端)提供参数化 SQL 查询接口,默认只读
- **Metabase + Superset**:自动导出 `metabase.duckdb` 目录文件 + S3 secrets,两个 BI 工具直接连上用
- **一条命令启动整个 stack**:`docker compose up -d --build`

```
┌──────────┐   HTTP/JSON   ┌─────────────────┐   httpfs    ┌────────────┐
│ Laravel  │ ────────────▶ │  duckdb-api :8080│ ──────────▶ │ S3 parquet │
└──────────┘               │  (GoFrame+DuckDB)│             └────────────┘
┌──────────┐               │  UI / REST       │  /local           ▲
│ 浏览器 UI │ ────────────▶ └───────┬─────────┘ 本地文件           │
└──────────┘                       │ 导出 /data/metabase.duckdb    │
┌──────────┐   JDBC(DuckDB driver) ▼        + /data/secrets       │
│ Metabase │ ──────────────────────┤                              │
│  :3000   │                       │                              │
├──────────┤  SQLAlchemy           │                              │
│ Superset │  (duckdb-engine)      │                              │
│  :8088   │ ──────────────────────┴──────────────────────────────┘
└──────────┘
```

## 快速开始

```bash
cp .env.example .env          # 修改 API_KEY!
docker compose up -d --build
```

- UI:<http://localhost:8080>(右上角填入 `.env` 里的 `API_KEY`)
- Metabase:<http://localhost:3000>(首次进入按向导初始化)
- Superset:<http://localhost:8088>(管理员账号密码在 `.env` 里,连接已自动注册好)
- 健康检查:`curl http://localhost:8080/healthz`

### 在 UI 里添加 orders 表

「数据表 → 添加表」,填:

| 字段 | 示例 |
|---|---|
| 表名 | `orders` |
| 格式 | Parquet(也支持 CSV / JSON / Avro,Avro 表的等效 SQL 会自动带 `INSTALL avro; LOAD avro` 并用 `read_avro()`) |
| 路径 | `s3://mybucket/orders/year=*/month=*/*.parquet` |
| Hive 分区 | ✅ |
| Region | `ap-southeast-2` |
| Access Key ID / Secret | 你的 S3 凭证 |

表单下方会实时显示等效 SQL:

```sql
INSTALL httpfs;
LOAD httpfs;

CREATE OR REPLACE SECRET "secret_orders" (
    TYPE s3,
    KEY_ID '...',
    SECRET '...',
    REGION 'ap-southeast-2',
    SCOPE 's3://mybucket'
);

CREATE OR REPLACE VIEW "orders" AS SELECT * FROM
    read_parquet('s3://mybucket/orders/year=*/month=*/*.parquet', hive_partitioning = true);
```

保存时服务会真正连 S3 验证一次(读取 parquet 元数据),失败会把 DuckDB 的报错原样返回。每个表使用独立的、按 bucket 限定 SCOPE 的 secret,所以不同表可以用不同账号/不同云(AWS / Cloudflare R2 / MinIO,填 Endpoint 即可)。

**等效 SQL 可以直接编辑**:改动后表单会标记「已手动编辑」,「保存并验证」将按你编辑的语句执行(执行后仍会 `DESCRIBE` 校验、失败自动回滚)。编辑后的 SQL 会作为该表的 `custom_sql` 保存,重启重建、Metabase/Superset 导出都用它(导出时 `CREATE SECRET` 自动升级为 `PERSISTENT`)。密钥可以保留 `'********'` 占位符,执行时自动替换为已保存的密钥,SQL 文本里不会存明文。点「↺ 重新生成」可回到按表单字段自动生成的模式。也可以完全用 SQL 定义一张表:只填表名 + 编辑 SQL(例如聚合视图、JOIN 多个数据源)。

### 本地文件表(Docker volume 挂载 / UI 上传)

除了 S3/https,也支持查询容器本地的文件,两种放文件的方式:

- **UI 上传**:添加表表单里点「📤 上传文件」,parquet/csv/json/avro 会直接传到容器内 `/local/`,路径自动填好——Docker 跑在远程服务器上时也能用,不需要登录服务器
- **volume 挂载**:把文件放进 Docker 宿主机的 `./localdata/`(目录可在 `.env` 里用 `LOCAL_DATA_DIR` 改,compose 会把它挂载为 duckdb-api / metabase / superset **三个容器**内的 `/local`)

然后添加表时路径填 `/local/sales.parquet` 或 `/local/orders/year=*/month=*/*.parquet`(不需要 S3 凭证),等效 SQL 就是 `CREATE VIEW ... AS SELECT * FROM read_parquet('/local/...')`。

要挂载更多目录(比如 NAS),在 `docker-compose.yml` 的 `duckdb-api`、`metabase`、`superset` 服务里**加同样的挂载行**(容器内路径必须一致,因为导出的视图引用的是绝对路径),BI 一侧建议 `:ro`。

> ⚠️ **远程 docker context**:如果 `docker context ls` 显示当前 context 指向远程主机(ssh://…),那么 `localhost:8080`、`./localdata` 等都在**远程那台机器**上——绑定挂载读的是远程文件系统,需要把 8080/3000 端口开放或做 SSH 端口转发后访问。这种场景下推荐直接用 UI 上传。

## API(供 Laravel 调用)

所有 `/api/*` 请求需要 `X-API-Key: <API_KEY>` 头(或 `Authorization: Bearer`)。响应统一为 `{"code":0,"message":"ok","data":{...}}`,非 0 即错误。

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/query` | 执行 SQL。body: `{"sql","args":[],"format":"assoc"\|"rows","max_rows":n}` |
| POST | `/api/upload` | 上传数据文件到 `/local`(multipart 字段 `file`),返回可注册的路径 |
| GET | `/api/tables` | 表列表(凭证脱敏) |
| POST | `/api/tables` | 注册表(body 即 UI 表单的 JSON,见下) |
| GET | `/api/tables/{name}` | 单个表定义 |
| PUT | `/api/tables/{name}` | 更新表(凭证留空/`********` 表示沿用旧值;`custom_sql` 非空则按该 SQL 执行,置空回到自动生成) |
| DELETE | `/api/tables/{name}` | 删除注册(不动 S3 数据) |
| GET | `/api/tables/{name}/schema` | `DESCRIBE` 结果 |
| GET | `/api/tables/{name}/preview?limit=50` | 采样数据 |

```bash
# 参数化查询(推荐,args 用 ? 占位)
curl -s http://localhost:8080/api/query \
  -H 'X-API-Key: changeme' -H 'Content-Type: application/json' \
  -d '{"sql":"SELECT month, count(*) n, sum(amount) total FROM orders WHERE year = ? GROUP BY 1 ORDER BY 1","args":[2024],"format":"assoc"}'

# 程序化注册表(等效 UI 操作)
curl -s http://localhost:8080/api/tables \
  -H 'X-API-Key: changeme' -H 'Content-Type: application/json' \
  -d '{
    "name": "orders",
    "format": "parquet",
    "path": "s3://mybucket/orders/year=*/month=*/*.parquet",
    "hive_partitioning": true,
    "s3": {
      "region": "ap-southeast-2",
      "access_key_id": "AKIA...",
      "secret_access_key": "..."
    }
  }'
```

默认**只读**:只放行 `SELECT / WITH / DESCRIBE / SHOW / SUMMARIZE / EXPLAIN / FROM / VALUES`,且禁止多语句。需要 ETL 写入时设 `ALLOW_WRITE=true`。

### Laravel 集成

把 [examples/laravel/DuckDb.php](examples/laravel/DuckDb.php) 拷到 `app/Services/`,配置:

```php
// config/services.php
'duckdb' => [
    'url' => env('DUCKDB_API_URL', 'http://localhost:8080'),
    'key' => env('DUCKDB_API_KEY', ''),
],
```

```dotenv
# Laravel 与本 stack 同一 docker network 时用服务名:
DUCKDB_API_URL=http://duckdb-api:8080
DUCKDB_API_KEY=changeme
```

使用:

```php
use App\Services\DuckDb;

$rows = DuckDb::select(
    'SELECT month, count(*) AS orders, sum(amount) AS revenue
     FROM orders WHERE year = ? GROUP BY 1 ORDER BY 1',
    [2024]
);
// Collection<array{month:..., orders:..., revenue:...}>

$tables = DuckDb::tables();
$schema = DuckDb::schema('orders');
```

> Laravel 项目和本 stack 不在一个 compose 里时,可把 `duckdb-api` 加入外部 network,或直接走宿主机端口 `http://host.docker.internal:8080`。

## Metabase 接入

1. 打开 <http://localhost:3000>,完成初始化向导(添加数据库时选 "I'll add my data later")
2. **Admin settings → Databases → Add database**,类型选 **DuckDB**
3. **Database file** 填:`/data/metabase.duckdb`
4. 如有 **read_only** 选项建议勾选;保存后 Sync

### 新表自动出现在 Metabase(推荐配置)

Metabase 的表列表来自它自己的**定时 schema 扫描**,所以默认情况下新注册的表要等扫描周期(或手动 Admin → Databases → Sync database schema / 重启)才可见。配置自动同步后,每次增删改表 duckdb-api 会立即调用 Metabase API 触发重新扫描:

1. Metabase → **Admin settings → Authentication → API keys → Create API key**(组选 Administrators)
2. 把 key 写进 `.env`:`METABASE_API_KEY=mb_xxx`,然后 `docker compose up -d duckdb-api`
3. 之后每次保存表,API 日志会输出 `metabase refreshed to ...`,新表几秒内出现在 Metabase

(`METABASE_URL` 默认 `http://metabase:3000`,`METABASE_DATABASE_ID` 默认自动发现 engine=duckdb 的库。)

实现细节:光触发 sync 是不够的——Metabase 的连接池长期持有旧目录文件(rename 替换对已打开的文件句柄不可见),其 DuckDB 驱动还按文件路径缓存数据库实例。所以每次导出会额外生成一份**带版本号的目录副本**(`/data/metabase-v<时间戳>.duckdb`,自动清理只保留最近两代),并通过 Metabase API 把连接的 `database_file` 指到新副本上——全新路径必然产生全新连接,再触发 sync 即可。配置自动同步后,Metabase 连接里显示的数据库文件是版本化路径,属正常现象;未配置 API key 时仍连固定的 `/data/metabase.duckdb`(需手动 Sync/重启才能看到新表)。

原理:每次在 UI 增删改表,服务都会重新导出

- `/data/metabase.duckdb` —— 包含所有视图定义(原子替换,写临时文件再 rename,不影响 Metabase 已打开的连接;改表后在 Metabase 里 **Sync database schema** 一下即可)
- `/data/secrets/*.duckdb_secret` —— 持久化的 S3 secrets,compose 已把它挂载到 Metabase 容器的 `~/.duckdb/stored_secrets`,DuckDB 会自动发现
- `/data/metabase-init.sql` —— 备用方案:如果你的驱动版本读不到共享 secrets(极少见的版本兼容问题),在 Metabase 连接设置的 init script / additional options 里指向这个文件即可

Metabase 首次查询 s3 表时,其内置 DuckDB 会自动下载 httpfs 扩展(容器需要外网,已用 volume 缓存,只下载一次)。

驱动版本在 [metabase/Dockerfile](metabase/Dockerfile) 里通过 `DUCKDB_DRIVER_VERSION` 控制,升级前看一眼[驱动 releases](https://github.com/motherduckdb/metabase_duckdb_driver/releases) 中标注的 Metabase 兼容版本。

## Superset 接入

开箱即用:`superset-init` 一次性容器会自动完成元数据库迁移、创建管理员(账号密码见 `.env` 的 `SUPERSET_ADMIN_*`)并注册好名为 **DuckDB Lakehouse** 的数据库连接(URI `duckdb:////data/metabase.duckdb`)。

打开 <http://localhost:8088> 登录后直接在 **SQL Lab** 里选 "DuckDB Lakehouse" 查询,或基于表建 Dataset / Chart / Dashboard。

- 镜像在 [superset/Dockerfile](superset/Dockerfile) 基础上加装了 [duckdb-engine](https://github.com/Mause/duckdb_engine)(SQLAlchemy 驱动)
- S3 secrets 通过 volume 挂载到 Superset 用户的 `~/.duckdb/stored_secrets` 自动发现;`/local` 本地文件与其他容器同路径挂载
- 增删改表后,新视图在**新连接**上生效;SQL Lab 每次查询新开连接,一般无感。若 Dataset 列表没更新,在数据库连接页点一下 Sync/刷新即可
- 手动加连接的话:Data → Database Connections → DuckDB,SQLAlchemy URI 填 `duckdb:////data/metabase.duckdb`;只读可在 Advanced → Engine Parameters 填 `{"connect_args":{"read_only":true}}`
- **务必**在 `.env` 里改 `SUPERSET_SECRET_KEY`(`openssl rand -base64 42`)和 `SUPERSET_ADMIN_PASSWORD`

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `API_KEY` | 空 | API 鉴权密钥;**为空则不鉴权,切勿暴露公网** |
| `PORT` | `8080` | 监听端口 |
| `DATA_DIR` | `/data`(容器)/ `./data` | tables.json、secrets、metabase.duckdb、扩展缓存 |
| `MAX_ROWS` | `10000` | 单次查询最大返回行数(超出返回 `truncated:true`) |
| `QUERY_TIMEOUT` | `300` | 查询超时(秒) |
| `ALLOW_WRITE` | `false` | 允许写语句 |
| `LOCAL_DATA_DIR` | `./localdata` | (compose 变量)Docker 宿主机本地文件目录,挂载为容器内 `/local` |
| `LOCAL_DIR` | `/local`(容器)/ `./localdata` | 本地文件表目录,也是 `/api/upload` 的保存位置 |
| `SUPERSET_SECRET_KEY` | (占位值) | Superset 会话加密密钥,**必须修改** |
| `SUPERSET_ADMIN_USERNAME/PASSWORD/EMAIL` | `admin`/`admin123`/… | Superset 管理员,首次初始化时创建 |
| `SUPERSET_PORT` | `8088` | Superset 端口映射 |

## 本地开发(不用 Docker)

```bash
go build -o duckdb-api . && API_KEY=dev DATA_DIR=./data ./duckdb-api
# 打开 http://localhost:8080
```

需要 CGO(go-duckdb 自带预编译静态库,macOS/Linux 直接可用)。

## 非 root 运行

duckdb-api 和 Metabase 镜像以 `.env` 里指定的非 root 用户运行(Superset 官方镜像本身就是非 root):

```dotenv
USER_NAME=app      # 只影响容器内 home 目录路径 /home/<name>
GROUP_NAME=app
USER_UID=1000      # 建议与 Docker 宿主机用户一致,方便读写 ./localdata 等 bind mount
GROUP_GID=1000
```

镜像内以该 uid/gid 建立账号(若基础镜像已占用该 uid,则把已有账号改名),修改后需要 `docker compose build`。

**volume 属主自动修复**:compose 里有一个一次性的 `volume-init` 服务,每次 `docker compose up` 会先以 root 跑一个瞬时 alpine 容器,把所有 volume(含 `./localdata` bind mount)chown 成 `.env` 里的 uid/gid,然后业务容器才启动——所以旧部署升级、修改 uid、换机器都**不需要手动迁移**。万一你绕过 compose 单独跑容器遇到 `data dir not writable`,按报错里给出的 chown 命令处理一次即可。

## 安全注意

- S3 凭证明文保存在 `duckdb_data` volume 的 `tables.json` / `secrets/` / `metabase-init.sql` 中 —— 请保护好宿主机与 volume,建议为该服务专门创建**只读、按 bucket 限权**的 IAM Key
- `POST /api/query` 接受任意 SQL(只读),API Key 是唯一防线,只应部署在内网 / 私有 network
- Laravel 端请始终用 `args` 参数占位,不要自己拼接 SQL

## 工作原理

- 服务进程内维护一个**共享的内存 DuckDB 实例**,启动时(以及每次增删改表时)按 `tables.json` 重建 `SECRET` + `VIEW`,数据本体永远留在 S3,按查询即时拉取
- 添加表 = `CREATE OR REPLACE SECRET`(带 bucket SCOPE)+ `CREATE OR REPLACE VIEW` + `DESCRIBE` 验证;失败自动回滚
- httpfs 扩展缓存在 `/data/extensions`,首次启动需要外网下载一次
