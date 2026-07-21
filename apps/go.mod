module codeberg.org/d-buckner/bloud/apps

go 1.25.0

require (
	codeberg.org/d-buckner/bloud/services/host-agent v0.0.0
	github.com/jackc/pgx/v5 v5.7.2
)

require (
	github.com/beevik/etree v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.38.0 // indirect
)

replace codeberg.org/d-buckner/bloud/services/host-agent => ../services/host-agent
