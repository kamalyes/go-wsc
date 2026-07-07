module github.com/kamalyes/go-wsc

go 1.25.0

require (
	github.com/alicebob/miniredis/v2 v2.35.0
	github.com/golang-jwt/jwt/v5 v5.2.2
	github.com/gorilla/websocket v1.4.2
	github.com/jpillora/backoff v1.0.0
	github.com/kamalyes/go-argus v0.2.1
	github.com/kamalyes/go-cachex v0.2.4-0.20260707070038-14739977e540
	github.com/kamalyes/go-config v0.21.5-0.20260629071806-990ad9d9b7f5
	github.com/kamalyes/go-logger v0.5.3
	github.com/kamalyes/go-pbmo v0.1.5-0.20260706095123-f6d04e0c255e
	github.com/kamalyes/go-sqlbuilder v0.5.6
	github.com/kamalyes/go-toolbox v0.15.4-0.20260706162621-92e82cf316fc
	github.com/redis/go-redis/v9 v9.21.0
	github.com/stretchr/testify v1.11.1
	google.golang.org/protobuf v1.36.11
	gorm.io/driver/mysql v1.6.0
	gorm.io/gorm v1.31.2
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dgraph-io/ristretto/v2 v2.4.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-sql-driver/mysql v1.8.1 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.9.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/arch v0.24.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	google.golang.org/grpc v1.82.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// 本地开发替换
// replace github.com/kamalyes/go-cachex => ../go-cachex

// replace github.com/kamalyes/go-config => ../go-config

// replace github.com/kamalyes/go-logger => ../go-logger

// replace github.com/kamalyes/go-toolbox => ../go-toolbox

// replace github.com/kamalyes/go-sqlbuilder => ../go-sqlbuilder

// replace github.com/kamalyes/go-argus => ../go-argus
