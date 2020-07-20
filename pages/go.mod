module engine/pages

go 1.14

replace (
	engine/core => ../core
	engine/history => ../history
	engine/pages => ./
	engine/whois => ../whois
)

require (
	engine/core v0.0.0-00010101000000-000000000000
	engine/history v0.0.0-00010101000000-000000000000
	github.com/go-chi/chi v4.1.1+incompatible
	github.com/jessevdk/go-flags v1.4.0
	github.com/prometheus/client_golang v1.6.0
	go.uber.org/zap v1.15.0
	golang.org/x/time v0.0.0-20200416051211-89c76fbcd5d1
)
