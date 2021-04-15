lint:
	golangci-lint run --enable-all --fix

fmt:
	gofmt -w .
