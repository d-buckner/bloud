module codeberg.org/d-buckner/bloud-v3/apps

go 1.24.0

require codeberg.org/d-buckner/bloud-v3/services/host-agent v0.0.0

require (
	github.com/beevik/etree v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.7.2 // indirect
	golang.org/x/crypto v0.47.0 // indirect
	golang.org/x/text v0.33.0 // indirect
)

replace codeberg.org/d-buckner/bloud-v3/services/host-agent => ../services/host-agent
