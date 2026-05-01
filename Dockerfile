FROM golang:1.24-alpine
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /usr/local/bin/safegit ./cmd/safegit
