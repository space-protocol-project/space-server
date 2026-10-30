module github.com/space-protocol-project/space-server

go 1.27.3

require (
	connectrpc.com/connect v1.21.1
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.32.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/jackpal/gateway v1.2.0
	github.com/pion/rtp v1.10.6
	github.com/pion/sdp/v3 v3.0.20
	github.com/pion/stun/v4 v4.0.1
	github.com/pion/turn/v5 v5.1.2
	github.com/pion/webrtc/v4 v4.2.25
	github.com/space-protocol-project/space-server/experiments/extensions v0.0.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
	golang.org/x/time v0.16.0
	google.golang.org/genproto/googleapis/api v0.0.0-20261019180522-ce908d29299b
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/pion/datachannel v1.6.3 // indirect
	github.com/pion/dtls/v3 v3.1.15 // indirect
	github.com/pion/ice/v4 v4.4.8 // indirect
	github.com/pion/interceptor v0.1.51 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.2.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.20 // indirect
	github.com/pion/sctp v1.12.0 // indirect
	github.com/pion/srtp/v3 v3.1.3 // indirect
	github.com/pion/transport/v5 v5.1.1 // indirect
	github.com/tetratelabs/wazero v1.12.0 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261019180522-ce908d29299b // indirect
)

replace github.com/space-protocol-project/space-server/experiments/extensions => ./experiments/extensions
