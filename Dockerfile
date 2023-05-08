FROM golang:1.19 AS build-env

WORKDIR /go/src/app
ADD . /go/src/app

RUN go test -mod=vendor -cover ./...
RUN CGO_ENABLED=0 go build -mod=vendor -o /go/bin/app


FROM gcr.io/distroless/static:latest-amd64
COPY --from=build-env /go/bin/app /app
CMD ["/app"]
