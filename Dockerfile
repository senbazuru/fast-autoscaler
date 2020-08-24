FROM golang:1.15-alpine3.12 AS build
ENV TZ Asia/Tokyo
RUN apk update && apk add git alpine-sdk tzdata
WORKDIR /go/src/github.com/miyaz/fast-autoscaler
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . .
RUN make deps clean build

FROM alpine:3.12
ENV TZ Asia/Tokyo
RUN apk --update --no-cache add ca-certificates tzdata
COPY --from=build /go/src/github.com/miyaz/fast-autoscaler/bin/fast-autoscaler /fast-autoscaler
CMD ["/fast-autoscaler"]
