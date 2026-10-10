module github.com/evdanil/gotest

go 1.26.0

require github.com/prometheus-community/pro-bing v0.9.2-0.20261008151844-bcbba8f702ab

require (
	github.com/ncruces/go-sqlite3-wasm/v6 v6.3.35304 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
)

require (
	github.com/google/uuid v1.6.0
	github.com/ncruces/go-sqlite3 v0.35.6
	golang.org/x/net v0.56.0
	golang.org/x/sys v0.48.0
)

replace github.com/prometheus-community/pro-bing => ./third_party/pro-bing
