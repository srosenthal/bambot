FROM golang:1.12

WORKDIR /bambot

COPY bambot.go .
RUN go get -v -t -d ./...

COPY bambot_test.go .
COPY test_files test_files
RUN go test -v ./...

CMD [ "go", "run", "bambot.go"]