package user_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestMainTErrorfRecordsFailure(t *testing.T) {
	var output bytes.Buffer
	mt := &mainT{output: &output}

	mt.Errorf("shutdown failed after %s", "10s")

	if got := mt.exitCode(0); got != 1 {
		t.Errorf("exitCode(0) = %d, want 1", got)
	}
	if got := output.String(); !strings.Contains(got, "shutdown failed after 10s") {
		t.Errorf("diagnostic = %q, want formatted message", got)
	}
}

func TestMainTRunCleanupsRecordsPanicAndContinuesLIFO(t *testing.T) {
	var output bytes.Buffer
	mt := &mainT{output: &output}
	var order []string
	mt.Cleanup(func() { order = append(order, "first") })
	mt.Cleanup(func() { panic("boom") })
	mt.Cleanup(func() { order = append(order, "last") })

	mt.runCleanups()

	if want := []string{"last", "first"}; !reflect.DeepEqual(order, want) {
		t.Errorf("cleanup order = %v, want %v", order, want)
	}
	if got := mt.exitCode(0); got != 1 {
		t.Errorf("exitCode(0) = %d, want 1 after cleanup panic", got)
	}
	if got := output.String(); !strings.Contains(got, "cleanup panicked: boom") {
		t.Errorf("diagnostic = %q, want cleanup panic", got)
	}
}

func TestMainTExitCodePreservesExistingFailure(t *testing.T) {
	mt := &mainT{output: new(bytes.Buffer)}
	if got := mt.exitCode(0); got != 0 {
		t.Errorf("exitCode(0) before failure = %d, want 0", got)
	}
	mt.Errorf("failure")
	if got := mt.exitCode(7); got != 7 {
		t.Errorf("exitCode(7) = %d, want 7", got)
	}
}
