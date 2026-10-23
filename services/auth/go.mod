module github.com/Desu-php/food/services/auth

go 1.27.2

require (
	github.com/Desu-php/food/platform v0.0.0
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/jackc/pgx/v5 v5.7.6
	golang.org/x/crypto v0.53.0
)

require (
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
)

replace github.com/Desu-php/food/platform => ../../platform
