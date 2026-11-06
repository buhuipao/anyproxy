module github.com/buhuipao/anyproxy

go 1.25.0

require (
	github.com/gorilla/websocket v1.5.3
	github.com/stretchr/testify v1.11.1
	github.com/things-go/go-socks5 v0.1.0
	google.golang.org/grpc v1.65.0
	google.golang.org/protobuf v1.34.2
	gopkg.in/yaml.v2 v2.4.0
)

require gopkg.in/natefinch/lumberjack.v2 v2.2.1

require (
	github.com/go-sql-driver/mysql v1.10.1
	github.com/google/uuid v1.6.0
	github.com/lib/pq v1.12.3
	github.com/sagernet/quic-go v0.52.0-beta.1
	github.com/sagernet/sing v0.7.14
	github.com/sagernet/sing-quic v0.5.2
	go.uber.org/zap v1.27.1
	golang.org/x/time v0.15.0
	modernc.org/sqlite v1.38.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gofrs/uuid/v5 v5.3.2 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/quic-go/qpack v0.5.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.41.0 // indirect
	golang.org/x/exp v0.0.0-20250408133849-7e4ce0ab07d0 // indirect
	golang.org/x/mod v0.27.0 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/sync v0.16.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
	golang.org/x/text v0.28.0 // indirect
	golang.org/x/tools v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240610135401-a8a62080eff3 // indirect
	modernc.org/libc v1.65.10 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rs/xid v1.6.0
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/sagernet/sing-quic => ./third_party/sing-quic
