FROM umputun/baseimage:buildgo-latest as build-pages

ADD . /build/pages
WORKDIR /build/pages

ENV GOFLAGS="-mod=vendor"

RUN apk add make

RUN make clean test build

FROM alpine:latest

WORKDIR /srv

COPY --from=build-pages /build/pages/bin/pages /srv/pages

EXPOSE 8080
EXPOSE 8090

ENTRYPOINT ["/srv/pages","--debug"]
