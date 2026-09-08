package ports

import (
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"google.golang.org/protobuf/proto"
)

// Core owns source canonicalization. The execution boundary verifies binding
// and aggregate size without interpreting file paths or granting filesystem access.
func ValidateRepairExecution(execution Execution) error {
	repair := execution.Repair
	if repair == nil && execution.Input.GetRepairTarget() == nil {
		return nil
	}
	if repair == nil || execution.Input.GetRepairTarget() == nil || execution.Input.GetIncidentId() == "" || execution.RepairSource == nil || len(execution.Input.GetOutputArtifactTypes()) != 0 || repair.GetTaskId() != execution.TaskID || !proto.Equal(repair.GetTarget(), execution.Input.GetRepairTarget()) || repair.GetSource().GetId() == "" || repair.GetSource().GetDigest() == "" || repair.GetBaseImage() == "" || len(repair.GetBuildCommand()) == 0 || len(repair.GetTestCommand()) == 0 {
		return NewRunError(ErrorKindInvalidInput, "repair input is missing or does not match its task", false, nil)
	}
	return ValidateRepairFiles(repair.GetSource().GetFiles())
}
func ValidateRepairFiles(files []*appv1.AppSourceFile) error {
	invalid := func() error {
		return NewRunError(ErrorKindProtocol, "repair files exceed their protocol bounds", false, nil)
	}
	if len(files) == 0 || len(files) > 128 {
		return invalid()
	}
	total := 0
	for _, file := range files {
		if file == nil || len(file.GetPath()) == 0 || len(file.GetPath()) > 240 || len(file.GetContent()) > 256*1024 {
			return invalid()
		}
		total += len(file.GetContent())
		if total > 512*1024 {
			return invalid()
		}
	}
	return nil
}
