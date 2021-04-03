package rootfs

import (
	"fmt"
	"testing"

	"github.com/combust-labs/firebuild-shared/build/commands"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
)

func TestClientHandlesStoppedServer(t *testing.T) {
	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	buildCtx := &WorkContext{
		ExecutableCommands: []commands.VMInitSerializableCommand{},
		ResourcesResolved:  make(Resources),
	}
	testServer, testClient, cleanupFunc := MustStartTestGRPCServer(t, logger, buildCtx)
	// close server
	testServer.Stop()
	defer cleanupFunc()
	// test client:
	assert.NotNil(t, testClient.Abort(fmt.Errorf("")))
	assert.NotNil(t, testClient.Commands())
	assert.NotNil(t, testClient.Ping())
	_, resourceErr := testClient.Resource("irrelevant")
	assert.NotNil(t, resourceErr)
	assert.NotNil(t, testClient.StdErr([]string{}))
	assert.NotNil(t, testClient.StdOut([]string{}))
	assert.NotNil(t, testClient.Success())
}
