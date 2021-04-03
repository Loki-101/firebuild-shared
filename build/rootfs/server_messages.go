package rootfs

// ClientMsgAborted is emitted by the server when the client aborts with an error.
type ClientMsgAborted struct {
	Error error
}

// ClientMsgStderr is emitted by the server when the client sends stderr contents.
type ClientMsgStderr struct {
	Lines []string
}

// ClientMsgStdout is emitted by the server when the client sends stdout contents.
type ClientMsgStdout struct {
	Lines []string
}

// ClientMsgSuccess is emitted by the server when the client finishes successfully.
type ClientMsgSuccess struct{}

// ControlMsgCommandsRequested is emitted by the server when the client requests the commands.
type ControlMsgCommandsRequested struct{}

// ControlMsgPingSent is emitted by the server when the client sends a ping request.
type ControlMsgPingSent struct{}
