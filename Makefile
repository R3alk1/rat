.PHONY: all proto clean

all: proto

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       internal/proto/service.proto

clean:
	rm -rf build internal/proto/*.pb.go
