module example.com/food/delivery

go 1.27.2

require (
	example.com/food/contracts v0.0.0
	github.com/jackc/pgx/v5 v5.7.6
	github.com/twmb/franz-go v1.22.2
	github.com/twmb/franz-go/pkg/kmsg v1.15.0
)

replace example.com/food/contracts => ../../contracts

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)
