package domain

import (
	"fmt"

	"github.com/google/uuid"
)

// NewID returns a time-ordered UUIDv7. It panics only if the system entropy
// source fails, which leaves the process unable to do anything useful anyway.
func NewID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		panic(fmt.Sprintf("generate UUIDv7: %v", err))
	}
	return id
}
