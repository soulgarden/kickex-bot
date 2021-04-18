FROM golang:1.16.2-alpine3.13 as builder

ENV GOPATH=/go

COPY . $GOPATH/src/github.com/soulgarden/kickex-bot

WORKDIR $GOPATH/src/github.com/soulgarden/kickex-bot

RUN go build -o /bin/bot

FROM alpine:3.13

RUN addgroup -S www-data \
    && adduser -S www-data -G www-data

COPY --from=builder --chown=www-data /bin/bot /bin/bot

USER www-data

CMD ["/bin/bot", "bot"]