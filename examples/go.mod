module examples

go 1.25.0

// 独立子 module：隔离 examples 的依赖（如 miniredis）不污染 go-wsc 主 module
// 与 kronos-cluster/examples 做法一致
require github.com/kamalyes/go-wsc v0.0.0

require (
	github.com/alicebob/miniredis/v2 v2.35.0
	github.com/gorilla/websocket v1.4.2
	github.com/kamalyes/go-cachex v0.3.6
	github.com/kamalyes/go-config v0.12.15
	github.com/kamalyes/go-toolbox v0.16.1
	github.com/redis/go-redis/v9 v9.22.0
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/dgraph-io/ristretto/v2 v2.4.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-sql-driver/mysql v1.8.1 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/golang-jwt/jwt/v5 v5.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/jpillora/backoff v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kamalyes/go-argus v0.3.1 // indirect
	github.com/kamalyes/go-logger v0.6.0 // indirect
	github.com/kamalyes/go-pbmo v0.2.0 // indirect
	github.com/kamalyes/go-sqlbuilder v0.6.5 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/arch v0.24.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	gorm.io/gorm v1.31.2 // indirect
)

replace github.com/kamalyes/go-wsc => ../
