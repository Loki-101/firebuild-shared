package rootfs

import (
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/combust-labs/firebuild-shared/build/commands"
	"github.com/combust-labs/firebuild-shared/build/resources"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
)

// TestServer wraps an instance of a server and provides testing
// utilities around it.
type TestServer interface {
	Start()
	Stop()
	FailedNotify() <-chan error
	FinishedNotify() <-chan struct{}
	ReadyNotify() <-chan struct{}

	Aborted() error
	ClientRequestedCommands() bool
	ReceivedStderr() []string
	ReceivedStdout() []string
	Succeeded() bool
}

// NewTestServer starts a new test server provider.
func NewTestServer(t *testing.T, logger hclog.Logger, cfg *GRPCServiceConfig, ctx *WorkContext) TestServer {
	return &testGRPCServerProvider{
		cfg:          cfg,
		ctx:          ctx,
		logger:       logger,
		stdErrOutput: []string{},
		stdOutOutput: []string{},
		chanAborted:  make(chan struct{}),
		chanFailed:   make(chan error, 1),
		chanFinished: make(chan struct{}),
		chanReady:    make(chan struct{}),
	}
}

type testGRPCServerProvider struct {
	cfg *GRPCServiceConfig
	ctx *WorkContext
	srv ServerProvider

	logger hclog.Logger

	abortError              error
	clientRequestedCommands bool
	stdErrOutput            []string
	stdOutOutput            []string
	success                 bool

	chanAborted  chan struct{}
	chanFailed   chan error
	chanFinished chan struct{}
	chanReady    chan struct{}

	isAbortedClosed bool
}

// Start starts a testing server.
func (p *testGRPCServerProvider) Start() {
	p.srv = New(p.cfg, p.logger)
	p.srv.Start(p.ctx)

	select {
	case <-p.srv.ReadyNotify():
		close(p.chanReady)
	case err := <-p.srv.FailedNotify():
		p.chanFailed <- err
		return
	}

	go func() {
	out:
		for {
			select {
			case <-p.srv.StoppedNotify():
				close(p.chanFinished)
				break out

			case message := <-p.srv.OnMessage():
				switch tmessage := message.(type) {
				case *ClientMsgAborted:
					p.abortError = tmessage.Error
					close(p.chanAborted)
				case *ClientMsgSuccess:
					if p.success {
						continue out
					}
					p.success = true
					go func() {
						p.srv.Stop()
					}()
				case *ClientMsgStderr:
					p.stdErrOutput = append(p.stdErrOutput, tmessage.Lines...)
				case *ClientMsgStdout:
					p.stdOutOutput = append(p.stdOutOutput, tmessage.Lines...)
				case *ControlMsgCommandsRequested:
					p.clientRequestedCommands = true
				}

			case <-p.chanAborted:
				if p.isAbortedClosed {
					continue
				}
				p.isAbortedClosed = true
				go func() {
					p.srv.Stop()
				}()
			}
		}
	}()
}

// Stop stops a testing server.
func (p *testGRPCServerProvider) Stop() {
	if p.srv != nil {
		p.srv.Stop()
	}
}

// FailedNotify returns a channel which will contain an error if the testing server failed to start.
func (p *testGRPCServerProvider) FailedNotify() <-chan error {
	return p.chanFailed
}

// FinishedNotify returns a channel which will be closed when the server is stopped.
func (p *testGRPCServerProvider) FinishedNotify() <-chan struct{} {
	return p.chanFinished
}

// ReadyNotify returns a channel which will be closed when the server is ready.
func (p *testGRPCServerProvider) ReadyNotify() <-chan struct{} {
	return p.chanReady
}

// Aborted returns the abort error, if client aborted.
func (p *testGRPCServerProvider) Aborted() error {
	return p.abortError
}

// ClientRequestedCommands returns true is the client requested messages from the server at least once.
func (p *testGRPCServerProvider) ClientRequestedCommands() bool {
	return p.clientRequestedCommands
}

// ReceivedStderr returns stderr received from the client.
func (p *testGRPCServerProvider) ReceivedStderr() []string {
	return p.stdErrOutput
}

// ReceivedStderr returns stdout received from the client.
func (p *testGRPCServerProvider) ReceivedStdout() []string {
	return p.stdOutOutput
}

// Succeeded returns true if the client finished successfully.
func (p *testGRPCServerProvider) Succeeded() bool {
	return p.success
}

// MustStartTestGRPCServer starts a test server and returns a client, a server and a server cleanup function.
// Fails test on any error.
func MustStartTestGRPCServer(t *testing.T, logger hclog.Logger, buildCtx *WorkContext) (TestServer, ClientProvider, func()) {
	grpcConfig := &GRPCServiceConfig{
		ServerName:        "test-grpc-server",
		BindHostPort:      "127.0.0.1:0",
		EmbeddedCAKeySize: 1024, // use this low for tests only! low value speeds up tests
	}
	testServer := NewTestServer(t, logger.Named("grpc-server"), grpcConfig, buildCtx)
	testServer.Start()
	select {
	case startErr := <-testServer.FailedNotify():
		t.Fatal("expected the GRPC server to start but it failed", startErr)
	case <-testServer.ReadyNotify():
		t.Log("GRPC server started and serving on", grpcConfig.BindHostPort)
	}

	clientConfig := &GRPCClientConfig{
		HostPort:  grpcConfig.BindHostPort,
		TLSConfig: grpcConfig.TLSConfigClient,
	}

	testClient, clientErr := NewClient(logger.Named("grpc-client"), clientConfig)
	if clientErr != nil {
		testServer.Stop()
		t.Fatal("expected the GRPC client, got error", clientErr)
	}
	return testServer, testClient, func() { testServer.Stop() }
}

// MustPutTestResource writes a test resource with a content under path.
// Creates intermediate directories and fails on any error.
func MustPutTestResource(t *testing.T, path string, contents []byte) {
	if err := os.MkdirAll(filepath.Dir(path), fs.ModePerm); err != nil {
		t.Fatal("failed creating parent directory for the resource, got error", err)
	}
	if err := ioutil.WriteFile(path, contents, fs.ModePerm); err != nil {
		t.Fatal("expected resource to be written, got error", err)
	}
}

// MustReadFromReader attempts reading from an input reader regardless of prior errors.
func MustReadFromReader(reader io.ReadCloser, _ error) ([]byte, error) {
	return ioutil.ReadAll(reader)
}

// MustBeAddCommand expects the next command from the client to be an ADD command.
func MustBeAddCommand(t *testing.T, testClient ClientProvider, expectedContents ...[]byte) {
	if addCommand, ok := testClient.NextCommand().(commands.Add); !ok {
		t.Fatal("expected ADD command")
	} else {
		MustReadResources(t, testClient, addCommand.Source, expectedContents...)

	}
}

// MustBeCopyCommand expects the next command from the client to be a COPY command.
func MustBeCopyCommand(t *testing.T, testClient ClientProvider, expectedContents ...[]byte) {
	if copyCommand, ok := testClient.NextCommand().(commands.Copy); !ok {
		t.Fatal("expected COPY command")
	} else {
		MustReadResources(t, testClient, copyCommand.Source, expectedContents...)
	}
}

// MustReadResources reads the resource from the client under the given path and compares the data with expected value.
func MustReadResources(t *testing.T, testClient ClientProvider, source string, expectedContents ...[]byte) {
	resourceChannel, err := testClient.Resource(source)
	if err != nil {
		t.Fatal("expected resource channel for COPY command, got error", err)
	}

	idx := 0
out:
	for {
		select {
		case item := <-resourceChannel:
			switch titem := item.(type) {
			case nil:
				break out // break out on nil
			case resources.ResolvedResource:
				resourceData, err := MustReadFromReader(titem.Contents())
				if err != nil {
					t.Fatal("expected resource to read, got error", err)
				}
				assert.Equal(t, expectedContents[idx], resourceData)
				idx = idx + 1
			case error:
				t.Fatal("received an error while reading ADD resource", titem)
			}
		}
	}

	assert.Equal(t, len(expectedContents), idx, "expected count of contents did not match count of resources read")

}

// MustBeRunCommand expects the next command from the client to be a RUN command.
func MustBeRunCommand(t *testing.T, testClient ClientProvider) {
	if _, ok := testClient.NextCommand().(commands.Run); !ok {
		t.Fatal("expected RUN command")
	}
}
