package rootfs

import (
	"testing"

	"github.com/hashicorp/go-hclog"
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
	ConsumedStderr() []string
	ConsumedStdout() []string
	Succeeded() bool
}

// NewTest starts a new test server provider.
func NewTestServer(t *testing.T, logger hclog.Logger, cfg *GRPCServiceConfig, ctx *WorkContext) *testGRPCServerProvider {
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
	srv ProviderServer

	logger hclog.Logger

	abortError   error
	stdErrOutput []string
	stdOutOutput []string
	success      bool

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
			case stdErrLine := <-p.srv.OnStderr():
				if stdErrLine == "" {
					continue
				}
				p.stdErrOutput = append(p.stdErrOutput, stdErrLine)
			case stdOutLine := <-p.srv.OnStdout():
				if stdOutLine == "" {
					continue
				}
				p.stdOutOutput = append(p.stdOutOutput, stdOutLine)
			case outErr := <-p.srv.OnAbort():
				p.abortError = outErr
				close(p.chanAborted)
			case <-p.srv.OnSuccess():
				if p.success {
					continue
				}
				p.success = true
				go func() {
					p.srv.Stop()
				}()
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

func (p *testGRPCServerProvider) Aborted() error {
	return p.abortError
}
func (p *testGRPCServerProvider) ConsumedStderr() []string {
	return p.stdErrOutput
}
func (p *testGRPCServerProvider) ConsumedStdout() []string {
	return p.stdOutOutput
}
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
		HostPort:       grpcConfig.BindHostPort,
		TLSConfig:      grpcConfig.TLSConfigClient,
		MaxRecvMsgSize: grpcConfig.MaxRecvMsgSize,
	}

	testClient, clientErr := NewClient(logger.Named("grpc-client"), clientConfig)
	if clientErr != nil {
		testServer.Stop()
		t.Fatal("expected the GRPC client, got error", clientErr)
	}
	return testServer, testClient, func() { testServer.Stop() }
}
