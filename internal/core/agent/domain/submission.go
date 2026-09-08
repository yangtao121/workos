package domain

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// MatchesSubmission compares caller-owned facts only. Provider and credential
// snapshots belong to the first admission and must survive later rebinding.
func (t Task) MatchesSubmission(owner, project string, input []byte) bool {
	if t.OwnerUserID != owner || t.ProjectID != project {
		return false
	}
	decode := func(raw []byte) (map[string]any, error) {
		if !json.Valid(raw) {
			return nil, ErrInvalid
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value map[string]any
		err := decoder.Decode(&value)
		return value, err
	}
	left, err := decode(t.Input)
	if err != nil || left == nil {
		return false
	}
	right, err := decode(input)
	return err == nil && right != nil && reflect.DeepEqual(left, right)
}
