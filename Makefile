lint:
	golangci-lint run --enable-all --fix

fmt:
	gofmt -w .

docker_up du:
	docker-compose up --build -d

docker_down dd:
	docker-compose down --timeout=10

docker_logs dl:
	docker-compose logs -f

build:
	docker build  . -f ./docker/bot/Dockerfile -t soulgarden/kickex-bot:1.0.44
	docker push soulgarden/kickex-bot:1.0.44

deploy_swarm ds:
	docker stack deploy kickex-bot -c docker-compose-swarm.yml --with-registry-auth --prune
