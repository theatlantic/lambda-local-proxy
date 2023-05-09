FROM --platform=$BUILDPLATFORM golang:1.19 AS build-env
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/
COPY . .
RUN go test -mod=vendor -cover ./...
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -mod=vendor -ldflags="-s -w" -a -installsuffix cgo -o app


FROM scratch
COPY --from=build-env /src/app /app
CMD ["/app"]
