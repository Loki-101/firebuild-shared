package rootfs

import (
	"bytes"
	"crypto/rand"
	"fmt"
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

func TestClientHandlesLargeFiles(t *testing.T) {

	tempDir, err := ioutil.TempDir("", "")
	assert.Nil(t, err)
	defer os.RemoveAll(tempDir)

	largeFileContent := getLargeFileContent(t, 10*1024*1024)

	MustPutTestResource(t, filepath.Join(tempDir, "large-file"), []byte(largeFileContent))

	logger := hclog.Default()
	logger.SetLevel(hclog.Debug)
	buildCtx := &WorkContext{
		ExecutableCommands: []commands.VMInitSerializableCommand{
			commands.Copy{
				OriginalCommand: "COPY large-file /etc/large-file",
				OriginalSource:  "large-file",
				Source:          "large-file",
				Target:          "/etc/large-file",
				User:            commands.DefaultUser(),
				Workdir:         commands.Workdir{Value: tempDir},
			},
		},
		ResourcesResolved: Resources{
			"large-file": []resources.ResolvedResource{
				resources.NewResolvedFileResourceWithPath(func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(largeFileContent)), nil
				},
					fs.FileMode(0755),
					"large-file",
					"/etc/large-file",
					commands.Workdir{Value: tempDir},
					commands.DefaultUser(),
					filepath.Join(tempDir, "large-file")),
			},
		},
	}

	testServer, testClient, cleanupFunc := MustStartTestGRPCServer(t, logger, buildCtx)
	defer cleanupFunc()

	assert.Nil(t, testClient.Commands())

	MustBeCopyCommand(t, testClient, largeFileContent)

	assert.Nil(t, testClient.Success())

	<-testServer.FinishedNotify()
}

func getLargeFileContent(t *testing.T, n int64) []byte {
	const alphanum = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var bs = make([]byte, n)
	rand.Read(bs)
	for i, b := range bs {
		bs[i] = alphanum[b%byte(len(alphanum))]
	}
	return bs
}
