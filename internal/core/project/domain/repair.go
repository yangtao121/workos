package domain

// RepairTarget freezes one active installation and its project's revision
// from the same read snapshot. It grants no additional authority.
type RepairTarget struct {
	Installation    Installation
	ProjectRevision int64
}
