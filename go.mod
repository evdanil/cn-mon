module github.com/evdanil/gotest

go 1.25.0

require github.com/prometheus-community/pro-bing v0.9.2-0.20261008151844-bcbba8f702ab

require (
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/prometheus-community/pro-bing => ./third_party/pro-bing
