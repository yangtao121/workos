package ids

import "github.com/google/uuid"

type Generator interface {
	New() string
}

type UUIDv7 struct{}

func (UUIDv7) New() string {
	value, err := uuid.NewV7()
	if err != nil {
		panic("generate UUIDv7: " + err.Error())
	}
	return value.String()
}
