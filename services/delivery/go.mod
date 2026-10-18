module github.com/Desu-php/food/services/delivery

go 1.27.2

replace github.com/Desu-php/food/contracts => ../../contracts

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)

require (
	github.com/Desu-php/food/contracts v0.0.0
	github.com/Desu-php/food/platform v0.0.0
	github.com/jackc/pgx/v5 v5.7.6
	github.com/redis/go-redis/v9 v9.7.0
	github.com/twmb/franz-go v1.22.2
	github.com/twmb/franz-go/pkg/kmsg v1.15.0
)

replace github.com/Desu-php/food/platform => ../../platform
