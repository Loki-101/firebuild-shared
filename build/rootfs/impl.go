package rootfs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/combust-labs/firebuild-shared/grpc/proto"
	"github.com/gofrs/uuid"
	"github.com/hashicorp/go-hclog"
)

// EventProvider provides the event subsriptions to the server executor.
// When client event occurs, a corresponding event will be sent via one of the channels.
type EventProvider interface {
	OnMessage() <-chan interface{}
}

type serverImplInterface interface {
	proto.RootfsServerServer
	EventProvider
	Stop()
}

type serverImpl struct {
	m       *sync.Mutex
	stopped bool

	logger        hclog.Logger
	serviceConfig *GRPCServiceConfig
	serverCtx     *WorkContext

	chanMessages chan interface{}
}

func newServerImpl(logger hclog.Logger, serverCtx *WorkContext, serviceConfig *GRPCServiceConfig) serverImplInterface {
	return &serverImpl{
		m:             &sync.Mutex{},
		logger:        logger,
		serviceConfig: serviceConfig,
		serverCtx:     serverCtx,
		chanMessages:  make(chan interface{}),
	}
}

func (impl *serverImpl) Abort(ctx context.Context, req *proto.AbortRequest) (*proto.Empty, error) {
	// handle stopped server
	impl.m.Lock()
	if impl.stopped {
		defer impl.m.Unlock()
		return &proto.Empty{}, fmt.Errorf("stopped")
	}
	impl.m.Unlock()

	impl.chanMessages <- &ClientMsgAborted{Error: errors.New(req.Error)}
	return &proto.Empty{}, nil
}

func (impl *serverImpl) Commands(ctx context.Context, _ *proto.Empty) (*proto.CommandsResponse, error) {
	// handle stopped server
	impl.m.Lock()
	if impl.stopped {
		defer impl.m.Unlock()
		return &proto.CommandsResponse{Command: []string{}}, fmt.Errorf("stopped")
	}
	impl.m.Unlock()

	impl.chanMessages <- &ControlMsgCommandsRequested{}
	response := &proto.CommandsResponse{Command: []string{}}
	for _, cmd := range impl.serverCtx.ExecutableCommands {
		commandBytes, err := json.Marshal(cmd)
		if err != nil {
			return response, err
		}
		response.Command = append(response.Command, string(commandBytes))
	}
	return response, nil
}

func (impl *serverImpl) Ping(ctx context.Context, req *proto.PingRequest) (*proto.PingResponse, error) {
	// handle stopped server
	impl.m.Lock()
	if impl.stopped {
		defer impl.m.Unlock()
		return &proto.PingResponse{Id: ""}, fmt.Errorf("stopped")
	}
	impl.m.Unlock()

	impl.chanMessages <- &ControlMsgPingSent{}
	return &proto.PingResponse{Id: req.Id}, nil
}

func (impl *serverImpl) Resource(req *proto.ResourceRequest, stream proto.RootfsServer_ResourceServer) error {
	// handle stopped server
	impl.m.Lock()
	if impl.stopped {
		defer impl.m.Unlock()
		return fmt.Errorf("stopped")
	}
	impl.m.Unlock()

	if ress, ok := impl.serverCtx.ResourcesResolved[req.Path]; ok {
		for _, resource := range ress {

			reader, err := resource.Contents()
			if err != nil {
				return err
			}

			impl.logger.Debug("sending resource data", "resource", resource.TargetPath())

			if resource.IsDir() {
				// by using this safe value, we leave space for other fields of the payload
				grpcDirResource := NewGRPCDirectoryResource(impl.serviceConfig.SafeClientMaxRecvMsgSize(), resource)
				outputChannel := grpcDirResource.WalkResource()
				for {
					payload := <-outputChannel
					if payload == nil {
						break
					}
					sendErr := stream.Send(payload)
					if sendErr != nil {
						// TODO: requires server abort
						impl.logger.Error("failed sending walk directory packet", "reason", sendErr)
						return sendErr
					}
				}
				continue
			}

			resourceUUID := uuid.Must(uuid.NewV4()).String()
			sendErr := stream.Send(&proto.ResourceChunk{
				Payload: &proto.ResourceChunk_Header{
					Header: &proto.ResourceChunk_ResourceHeader{
						SourcePath:    resource.SourcePath(),
						TargetPath:    resource.TargetPath(),
						FileMode:      int64(resource.TargetMode()),
						IsDir:         resource.IsDir(),
						TargetUser:    resource.TargetUser().Value,
						TargetWorkdir: resource.TargetWorkdir().Value,
						Id:            resourceUUID,
					},
				},
			})
			if sendErr != nil {
				// TODO: requires server abort
				impl.logger.Error("Failed sending header", "reason", sendErr)
				return sendErr
			}

			// by using this safe value, we leave space for other fields of the payload
			buffer := make([]byte, impl.serviceConfig.SafeClientMaxRecvMsgSize())

			for {
				readBytes, err := reader.Read(buffer)
				if readBytes == 0 && err == io.EOF {
					sendErr := stream.Send(&proto.ResourceChunk{
						Payload: &proto.ResourceChunk_Eof{
							Eof: &proto.ResourceChunk_ResourceEof{
								Id: resourceUUID,
							},
						},
					})
					if sendErr != nil {
						// TODO: requires server abort
						impl.logger.Error("Failed sending eof", "reason", sendErr)
						return sendErr
					}
					break
				} else {
					payload := buffer[0:readBytes]
					hash := sha256.Sum256(payload)
					sendErr := stream.Send(&proto.ResourceChunk{
						Payload: &proto.ResourceChunk_Chunk{
							Chunk: &proto.ResourceChunk_ResourceContents{
								Chunk:    payload,
								Checksum: hash[:],
								Id:       resourceUUID,
							},
						},
					})
					if sendErr != nil {
						// TODO: requires server abort
						impl.logger.Error("Failed sending chunk", "reason", sendErr)
						return sendErr
					}
				}
			}
		}

	} else {
		return fmt.Errorf("not found: '%s/%s'", req.Stage, req.Path)
	}
	return nil
}

func (impl *serverImpl) StdErr(ctx context.Context, req *proto.LogMessage) (*proto.Empty, error) {
	// handle stopped server
	impl.m.Lock()
	if impl.stopped {
		defer impl.m.Unlock()
		return &proto.Empty{}, fmt.Errorf("stopped")
	}
	impl.m.Unlock()

	impl.chanMessages <- &ClientMsgStderr{Lines: req.Line}
	return &proto.Empty{}, nil
}

func (impl *serverImpl) StdOut(ctx context.Context, req *proto.LogMessage) (*proto.Empty, error) {
	// handle stopped server
	impl.m.Lock()
	if impl.stopped {
		defer impl.m.Unlock()
		return &proto.Empty{}, fmt.Errorf("stopped")
	}
	impl.m.Unlock()

	impl.chanMessages <- &ClientMsgStdout{Lines: req.Line}
	return &proto.Empty{}, nil
}

func (impl *serverImpl) Stop() {
	impl.m.Lock()
	if impl.stopped {
		impl.m.Unlock()
		return
	}

	impl.stopped = true
	impl.m.Unlock()
}

func (impl *serverImpl) Success(ctx context.Context, _ *proto.Empty) (*proto.Empty, error) {
	// handle stopped server
	impl.m.Lock()
	if impl.stopped {
		defer impl.m.Unlock()
		return &proto.Empty{}, fmt.Errorf("stopped")
	}
	impl.m.Unlock()

	impl.chanMessages <- &ClientMsgSuccess{}
	return &proto.Empty{}, nil
}

func (impl *serverImpl) OnMessage() <-chan interface{} {
	return impl.chanMessages
}
