# Sample build for scripts/footprint.sh: a small Go compile, typical of a real app build.
FROM golang:1.23-alpine
WORKDIR /src
RUN printf 'package main\nimport "fmt"\nfunc main() { fmt.Println("ok") }\n' > main.go && \
    go mod init sample && go build -o /app .
