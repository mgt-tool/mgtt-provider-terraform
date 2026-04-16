package tfclassify

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/mgt-tool/mgtt/sdk/provider"
)

func TestClassify_NotFound(t *testing.T) {
	cases := []string{
		"No state file was found!",
		"workspace \"prod\" does not exist",
		"Resource not found in state",
		"No configuration files found!",
	}
	for _, msg := range cases {
		err := Classify(msg, errors.New("exit 1"))
		if !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("%q → want ErrNotFound, got %v", msg, err)
		}
	}
}

func TestClassify_Forbidden(t *testing.T) {
	cases := []string{
		"Error: AccessDenied: User is not authorized",
		"Error: forbidden: API call denied",
		"Error: Unauthorized",
		"NoValidCredentialSources: unable to find credentials",
		"Unable to locate credentials",
	}
	for _, msg := range cases {
		err := Classify(msg, errors.New("exit 1"))
		if !errors.Is(err, provider.ErrForbidden) {
			t.Errorf("%q → want ErrForbidden, got %v", msg, err)
		}
	}
}

func TestClassify_Transient(t *testing.T) {
	cases := []string{
		"Error acquiring the state lock",
		"Error: state lock already held",
		"dial tcp: lookup s3.amazonaws.com: no such host",
		"connection refused",
		"TLS handshake timeout",
		"context deadline exceeded",
		"i/o timeout",
	}
	for _, msg := range cases {
		err := Classify(msg, errors.New("exit 1"))
		if !errors.Is(err, provider.ErrTransient) {
			t.Errorf("%q → want ErrTransient, got %v", msg, err)
		}
	}
}

func TestClassify_BinaryMissingFallsThroughToEnv(t *testing.T) {
	err := Classify("", &exec.Error{Name: "terraform", Err: exec.ErrNotFound})
	if !errors.Is(err, provider.ErrEnv) {
		t.Fatalf("want ErrEnv, got %v", err)
	}
}

func TestClassify_NoErrorReturnsNil(t *testing.T) {
	if err := Classify("whatever", nil); err != nil {
		t.Fatal(err)
	}
}

func TestClassify_UnknownFallsThroughToEnv(t *testing.T) {
	err := Classify("Error: something terraform-internal we don't recognize", errors.New("exit 1"))
	if !errors.Is(err, provider.ErrEnv) {
		t.Fatalf("want ErrEnv, got %v", err)
	}
}
