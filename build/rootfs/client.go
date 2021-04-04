package rootfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"strings"

	"github.com/combust-labs/firebuild-shared/build/commands"
	"github.com/combust-labs/firebuild-shared/grpc/proto"
	"github.com/gofrs/uuid"
	"github.com/hashicorp/go-hclog"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ClientProvider defines a GRPC client behaviour.
type ClientProvider interface {
	// Abort aborts the client with error.
	Abort(error) error
	// Commands requests the processable commands from the server.
	Commands() error
	// NextCommand returns the next command to process, Commands() must be called first.
	NextCommand() commands.VMInitSerializableCommand
	// Ping sends a ping message to the server, if the response ID does not match, returns an error.
	Ping() error
	// Resource loads the resource identified by a path from the server.
	Resource(string) (chan interface{}, error)
	// StdErr sends stderr lines to the server.
	StdErr([]string) error
	// StdOut sends stdout lines to the server.
	StdOut([]string) error
	// Success finishes the client with success.
	Success() error
}

// GRPCClientConfig is the client configuration.
type GRPCClientConfig struct {
	// HostPort to connect to.
	HostPort string
	// TLSConfig is the optional TLS configuration to use when connecting to the server.
	TLSConfig *tls.Config
	// MaxRecvMsgSize is the maximum message size the client can safely handle.
	MaxRecvMsgSize int
}

// WithDefaultsApplied applies default configuration values to unconfigured properties.
func (c *GRPCClientConfig) WithDefaultsApplied() *GRPCClientConfig {
	if c.MaxRecvMsgSize == 0 {
		c.MaxRecvMsgSize = DefaultMaxMsgSize
	}
	return c
}

// NewClient returns a new default client provider implementation.
func NewClient(logger hclog.Logger, cfg *GRPCClientConfig) (ClientProvider, error) {
	cfg = cfg.WithDefaultsApplied()
	grpcConn, err := grpc.Dial(cfg.HostPort,
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(cfg.MaxRecvMsgSize)),
		grpc.WithTransportCredentials(credentials.NewTLS(cfg.TLSConfig)))

	if err != nil {
		return nil, err
	}

	return &defaultClient{logger: logger, underlying: proto.NewRootfsServerClient(grpcConn)}, nil
}

type defaultClient struct {
	logger          hclog.Logger
	fetchedCommands []commands.VMInitSerializableCommand
	underlying      proto.RootfsServerClient
}

// Abort aborts the client with error.
func (c *defaultClient) Abort(input error) error {
	_, err := c.underlying.Abort(context.Background(), &proto.AbortRequest{Error: input.Error()})
	return err
}

// Commands requests the processable commands from the server.
func (c *defaultClient) Commands() error {
	c.fetchedCommands = []commands.VMInitSerializableCommand{}
	response, err := c.underlying.Commands(context.Background(), &proto.Empty{})
	if err != nil {
		return err
	}
	for _, cmd := range response.Command {
		rawItem := map[string]interface{}{}
		if err := json.Unmarshal([]byte(cmd), &rawItem); err != nil {
			return err
		}

		if originalCommandString, ok := rawItem["OriginalCommand"]; ok {
			if strings.HasPrefix(fmt.Sprintf("%s", originalCommandString), "ADD") {
				command := commands.Add{}
				if err := mapstructure.Decode(rawItem, &command); err != nil {
					return errors.Wrap(err, "found ADD but did not deserialize")
				}
				c.fetchedCommands = append(c.fetchedCommands, command)
			} else if strings.HasPrefix(fmt.Sprintf("%s", originalCommandString), "COPY") {
				command := commands.Copy{}
				if err := mapstructure.Decode(rawItem, &command); err != nil {
					return errors.Wrap(err, "found COPY but did not deserialize")
				}
				c.fetchedCommands = append(c.fetchedCommands, command)
			} else if strings.HasPrefix(fmt.Sprintf("%s", originalCommandString), "RUN") {
				command := commands.Run{}
				if err := mapstructure.Decode(rawItem, &command); err != nil {
					return errors.Wrap(err, "found RUN but did not deserialize")
				}
				c.fetchedCommands = append(c.fetchedCommands, command)
			} else {
				c.logger.Warn("unexpected command received from grpc", "command", rawItem)
			}
		}
	}
	return nil
}

// NextCommand returns the next command to process, Commands() must be called first.
func (c *defaultClient) NextCommand() commands.VMInitSerializableCommand {
	if len(c.fetchedCommands) == 0 {
		return nil
	}
	result := c.fetchedCommands[0]
	if len(c.fetchedCommands) == 1 {
		c.fetchedCommands = []commands.VMInitSerializableCommand{}
	} else {
		c.fetchedCommands = c.fetchedCommands[1:]
	}
	return result
}

// Ping sends a ping message to the server, if the response ID does not match, returns an error.
func (c *defaultClient) Ping() error {
	pingID := uuid.Must(uuid.NewV4()).String()
	response, err := c.underlying.Ping(context.Background(), &proto.PingRequest{Id: pingID})
	if err != nil {
		return err
	}
	if response.Id != pingID {
		return fmt.Errorf("ping response invalid")
	}
	return nil
}

// Resource loads the resource identified by a path from the server.
func (c *defaultClient) Resource(input string) (chan interface{}, error) {

	chanResources := make(chan interface{})

	resourceClient, err := c.underlying.Resource(context.Background(), &proto.ResourceRequest{Path: input})
	if err != nil {
		return nil, err
	}

	go func() {

		var currentResource *grpcResolvedResource

	out:
		for {
			response, err := resourceClient.Recv()

			if response == nil {
				resourceClient.CloseSend()
				break
			}

			// yes, err check after response check
			if err != nil {
				chanResources <- errors.Wrap(err, "failed reading chunk")
				break out
			}

			switch tresponse := response.GetPayload().(type) {
			case *proto.ResourceChunk_Eof:
				chanResources <- currentResource
			case *proto.ResourceChunk_Chunk:
				hash := sha256.Sum256(tresponse.Chunk.Chunk)
				if string(hash[:]) != string(tresponse.Chunk.Checksum) {
					chanResources <- errors.Wrap(err, "chunk checksum did not match")
					break out
				}
				currentResource.contents.Grow(len(tresponse.Chunk.Chunk))
				currentResource.contents.Write(tresponse.Chunk.Chunk)
			case *proto.ResourceChunk_Header:
				currentResource = &grpcResolvedResource{
					contents:      bytes.NewBuffer([]byte{}),
					isDir:         tresponse.Header.IsDir,
					sourcePath:    tresponse.Header.SourcePath,
					targetMode:    fs.FileMode(tresponse.Header.FileMode),
					targetPath:    tresponse.Header.TargetPath,
					targetUser:    tresponse.Header.TargetUser,
					targetWorkdir: tresponse.Header.TargetWorkdir,
				}
			}
		}

		close(chanResources)

	}()

	return chanResources, nil
}

// StdErr sends stderr lines to the server.
func (c *defaultClient) StdErr(input []string) error {
	_, err := c.underlying.StdErr(context.Background(), &proto.LogMessage{Line: input})
	return err
}

// StdOut sends stdout lines to the server.
func (c *defaultClient) StdOut(input []string) error {
	_, err := c.underlying.StdOut(context.Background(), &proto.LogMessage{Line: input})
	return err
}

// Success finishes the client with success.
func (c *defaultClient) Success() error {
	_, err := c.underlying.Success(context.Background(), &proto.Empty{})
	return err
}

// --
// test resolved resource

type grpcResolvedResource struct {
	contents      *bytes.Buffer
	isDir         bool
	sourcePath    string
	targetMode    fs.FileMode
	targetPath    string
	targetUser    string
	targetWorkdir string
}

func (r *grpcResolvedResource) Contents() (io.ReadCloser, error) {
	return ioutil.NopCloser(r.contents), nil
}

func (r *grpcResolvedResource) IsDir() bool {
	return r.isDir
}

func (r *grpcResolvedResource) ResolvedURIOrPath() string {
	return fmt.Sprintf("grpc://%s", r.sourcePath)
}

func (r *grpcResolvedResource) SourcePath() string {
	return r.sourcePath
}
func (r *grpcResolvedResource) TargetMode() fs.FileMode {
	return r.targetMode
}
func (r *grpcResolvedResource) TargetPath() string {
	return r.targetPath
}
func (r *grpcResolvedResource) TargetWorkdir() commands.Workdir {
	return commands.Workdir{Value: r.targetWorkdir}
}
func (r *grpcResolvedResource) TargetUser() commands.User {
	return commands.User{Value: r.targetUser}
}
