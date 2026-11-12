FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pelosi-mirror .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /pelosi-mirror /usr/local/bin/pelosi-mirror
VOLUME /data
ENTRYPOINT ["/usr/local/bin/pelosi-mirror"]
