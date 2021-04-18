lint:
	golangci-lint run --enable-all --fix

fmt:
	gofmt -w .

docker_up du:
	docker-compose up --build -d

docker_down dd:
	docker-compose down --timeout=10

build:
	docker build  . -f ./docker/bot/Dockerfile -t soulgarden/kickex-bot:1.0.4
	docker push soulgarden/kickex-bot:1.0.4
