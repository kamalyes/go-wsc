module github.com/kamalyes/go-wsc

go 1.26.0

require (
	github.com/alicebob/miniredis/v2 v2.40.0
	github.com/gorilla/websocket v1.4.2
	github.com/jpillora/backoff v1.1.0
	github.com/kamalyes/go-argus v0.5.1
	github.com/kamalyes/go-cachex v0.6.0
	github.com/kamalyes/go-config v0.25.1
	github.com/kamalyes/go-logger v0.10.0
	github.com/kamalyes/go-pbmo v0.3.1
	github.com/kamalyes/go-sqlbuilder v0.9.0
	github.com/kamalyes/go-toolbox v0.19.4
	github.com/redis/go-redis/v9 v9.23.0
	github.com/stretchr/testify v1.12.1
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
	gorm.io/gorm v1.31.2
)

require (
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kamalyes/go-toolbox/pkg/protox v0.19.3 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	modernc.org/libc v1.22.5 // indirect
	modernc.org/mathutil v1.5.0 // indirect
	modernc.org/memory v1.5.0 // indirect
	modernc.org/sqlite v1.23.1 // indirect
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgraph-io/ristretto/v2 v2.4.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/glebarez/sqlite v1.11.0
	github.com/go-sql-driver/mysql v1.8.1 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260817212433-ac3dfec99bb1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// 本地开发替换
// replace github.com/kamalyes/go-cachex => ../go-cachex

// replace github.com/kamalyes/go-logger => ../go-logger

// replace github.com/kamalyes/go-toolbox => ../go-toolbox

// replace github.com/kamalyes/go-sqlbuilder => ../go-sqlbuilder

// replace github.com/kamalyes/go-argus => ../go-argus
