[![Go Report Card](https://goreportcard.com/badge/github.com/soulgarden/kickex-bot)](https://goreportcard.com/report/github.com/soulgarden/kickex-bot)

Trading bot for [kickex](https://kickex.com/) exchange

Implemented 3 strategies:

* buy
* spread
* arbitrage

## Project installation

* [Download](https://www.docker.com/get-started) and install docker
* Copy ./conf/conf.example.json to ./conf/conf.remote.json and edit it

To run in docker swarm 

* Review ./docker-compose-swarm.yml and enable/disable strategies
* Run `docker swarm init`
* Run `make deploy_swarm`