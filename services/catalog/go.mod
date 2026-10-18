module example.com/food/catalog

go 1.27.2

require (
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)

replace example.com/food/contracts => ../../contracts

require (
	example.com/food/contracts v0.0.0
	example.com/food/platform v0.0.0
	github.com/jackc/pgx/v5 v5.7.6
)

replace example.com/food/platform => ../../platform
