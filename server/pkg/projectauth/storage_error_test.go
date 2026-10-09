package projectauth

import (
	"errors"
	"testing"
)

func TestAuthorizationStorageErrorPreservesCause(t *testing.T) {
	cause := errors.New("repository read failed")
	err := authorizationStorageError(cause)
	if !errors.Is(err, ErrStorageUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("storage error must retain classification and cause: %v", err)
	}
}
