module github.com/goadesign/scantle/examples/orders-eval

go 1.27.3

replace github.com/goadesign/scantle/examples/orders => ../orders

require (
	github.com/goadesign/scantle/examples/orders v0.0.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/stretchr/testify v1.12.1
	goa.design/goa/v3 v3.35.1-0.20261023134129-12a5635f9dd0
)

require (
	github.com/go-chi/chi/v5 v5.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	goa.design/goa-ai v0.89.1-0.20261028052713-99d852887d11 // indirect
	golang.org/x/net v0.61.0 // indirect
)
