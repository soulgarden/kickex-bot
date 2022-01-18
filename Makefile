lint:
	golangci-lint run --enable-all --fix

fmt:
	gofmt -w .

test:
	ROOT_DIR=${PWD} go test -failfast ./...

build:
	docker build  . -f ./docker/bot/Dockerfile -t soulgarden/kickex-bot:1.0.63
	docker push soulgarden/kickex-bot:1.0.63

build_fluentd:
	docker build ./docker/fluentd -t soulgarden/kickex-bot:fluentd
	docker push soulgarden/kickex-bot:fluentd

deploy_swarm ds:
	docker stack deploy kickex-bot -c docker-compose-swarm.yml --with-registry-auth --prune
