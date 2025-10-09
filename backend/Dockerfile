FROM golang:1.23 AS build

# Context path is the root of the repository

WORKDIR /go/src/app

# Copy the current directory (backend) first
COPY ./backend ./backend/

# Copy required modules from relative paths
COPY ./core ./core/
COPY ./grpc ./grpc/
COPY ./trace ./trace/
COPY ./vcs ./vcs/

ENV GO111MODULE on

# Build from the backend directory which contains the main.go
RUN cd backend && go install -v ./...

FROM alpine:3.14

# Copy the built binary
COPY --from=build /go/bin/crawlab /go/bin/crawlab
