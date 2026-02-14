.PHONY: all proto clean

all: proto build-linux build-windows

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       internal/proto/service.proto

build-linux:
	mkdir -p build
	go build -o build/c2 ./cmd/c2
	go build -o build/mgmt ./cmd/mgmt

build-windows:
	mkdir -p build
	GOOS=windows GOARCH=amd64 go build -ldflags="-H=windowsgui -s -w" -o build/client.exe ./cmd/client/main_windows.go

clean:
	rm -rf build internal/proto/*.pb.go