package workspace

type Mount struct {
	OwnerUserID, ProjectID, Path string
	ReadOnly                     bool
}
