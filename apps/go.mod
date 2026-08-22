module codeberg.org/d-buckner/bloud/apps

go 1.25.0

require (
	codeberg.org/d-buckner/bloud/services/host-agent v0.0.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/beevik/etree v1.6.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.7.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace codeberg.org/d-buckner/bloud/services/host-agent => ../services/host-agent
