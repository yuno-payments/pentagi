package k8sbackend

import (
	"errors"
	"testing"

	utilexec "k8s.io/client-go/util/exec"
)

func TestBackend_ParseExecExitCode_ClassifiesStreamErrors(t *testing.T) {
	if code, err := parseExecExitCode(nil); err != nil || code != 0 {
		t.Fatalf("nil error must be exit 0, got code=%d err=%v", code, err)
	}

	code, err := parseExecExitCode(utilexec.CodeExitError{Err: errors.New("boom"), Code: 7})
	if err != nil || code != 7 {
		t.Fatalf("CodeExitError must yield its code, got code=%d err=%v", code, err)
	}

	code, err = parseExecExitCode(errors.New("command terminated with non-zero exit code: ... exit code 42"))
	if err != nil || code != 42 {
		t.Fatalf("status message must yield 42, got code=%d err=%v", code, err)
	}

	code, err = parseExecExitCode(errors.New("dial tcp: connection refused"))
	if err == nil || code != -1 {
		t.Fatalf("transport error must surface as code=-1 + err, got code=%d err=%v", code, err)
	}
}

func TestBackend_ExitCodeFromStatusMessage_ParsesTrailingInteger(t *testing.T) {
	if n, ok := exitCodeFromStatusMessage("blah exit code 13"); !ok || n != 13 {
		t.Fatalf("want 13/true, got %d/%v", n, ok)
	}
	if _, ok := exitCodeFromStatusMessage("no code here"); ok {
		t.Fatalf("must not match when there is no 'exit code N'")
	}
}
