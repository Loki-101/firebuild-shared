.PHONY: genproto
genproto:
	protoc -I=./grpc/proto \
		--go_out=./grpc/proto \
			--go_opt=paths=source_relative \
		--go-grpc_out=./grpc/proto \
			--go-grpc_opt=paths=source_relative \
			--go-grpc_opt=require_unimplemented_servers=false \
		./grpc/proto/rootfs_server.proto

.PHONY: prototools
prototools:
	go get -u github.com/golang/protobuf/proto \
		github.com/golang/protobuf/protoc-gen-go \
		google.golang.org/grpc \
		google.golang.org/protobuf/cmd/protoc-gen-go \
		google.golang.org/grpc/cmd/protoc-gen-go-grpc
	go install google.golang.org/protobuf/cmd/protoc-gen-go \
		google.golang.org/grpc/cmd/protoc-gen-go-grpc
	go mod tidy

.PHONY: release
release:
	curl -sL https://raw.githubusercontent.com/radekg/git-release/master/git-release --output /tmp/git-release
	chmod +x /tmp/git-release
	/tmp/git-release --repository-path=${GOPATH}/src/github.com/combust-labs/firebuild-mmds
	make build-from-latest-tag
	rm -rf /tmp/git-release