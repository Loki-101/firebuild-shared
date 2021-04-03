package env

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildEnv(t *testing.T) {
	inputMap := map[string]string{
		"ENV_1": "value 1",
		"ENV_2": "value 2",
	}
	buildEnv := NewBuildEnv()
	for k, v := range inputMap {
		buildEnv.Put(k, v)
	}
	assert.Equal(t, inputMap, buildEnv.Snapshot())

	expected := "Expecting value 1 and value 2 but not apkArch=\"$(apk --print-arch)\" && case \"${apkArch}\""
	output := buildEnv.Expand("Expecting ${ENV_1} and ${ENV_2} but not apkArch=\"$(apk --print-arch)\" && case \"${apkArch}\"")
	assert.Equal(t, output, expected)

}

func TestCustomExpanderHandlesAwkCommand(t *testing.T) {

	buildEnv := NewBuildEnv()

	// while os.Expand with a mapper would have to make a choice to either wrap with {} or not
	// to be on the safe side, use custom expander so we always preserve the state of {} after $
	// this is something the system os.Expand is not capable of

	// here, the input $NF has no surrounding {}
	expected := "dpkgArch=\"$(dpkg --print-architecture | awk -F- '{ print $NF }')\";"
	output := buildEnv.Expand(expected)
	assert.Equal(t, output, expected)

	// bu here it does, we want to preserve that:
	expected2 := "apkArch=\"$(apk --print-arch)\" && case \"${apkArch:-abc}\""
	output2 := buildEnv.Expand(expected2)
	assert.Equal(t, output2, expected2)

}
